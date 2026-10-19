// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package storefactory turns a config.Storage union into the storages.Storager
// the backends write through. It is the one place a configured path becomes a
// store root, which makes it the one place the store namespace and the segments
// below it are appended.
//
// Root opens <path>/arcave; ArcaveProject, Session and ComposeProject each descend
// one gated segment, and New chains the four.
package storefactory

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sync"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/azure"
	"github.com/greenmaskio/storages/directory"
	"github.com/greenmaskio/storages/s3"
	"github.com/greenmaskio/storages/ssh"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// storeNamespace is the static segment every arm must root its store under,
// appended to whatever the config names. It is not configurable: it guarantees
// everything under the store root is arcave's own data, bounding every delete
// to it — point the path at /etc and arcave operates in /etc/arcave.
const storeNamespace = "arcave"

// warnHostKeyOnce keeps the ssh arm's host-key warning to one per process.
var warnHostKeyOnce sync.Once

// The static segments between the namespace and the compose project. They name what the
// value after them is, so one configured path can hold every project a user
// points at it and a reader of the tree can tell a tenant from a lifetime
// without knowing arcave's vocabulary.
const (
	projectsNamespace = "projects"
	sessionsNamespace = "sessions"
	composeNamespace  = "compose"
)

// New opens the store at <path>/arcave/projects/<project>/sessions/<session>/compose/<compose-project>,
// validating every segment, outermost first, before anything is opened.
func New(
	ctx context.Context,
	cfg config.Storage,
	projectRoot string,
	arcaveProject model.ArcaveProjectName,
	session model.SessionID,
	composeProject model.DockerComposeProjectName,
) (storages.Storager, error) {
	if err := arcaveProject.Validate(); err != nil {
		return nil, fmt.Errorf("validate arcave project name: %w", err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	if err := validateComposeProjectName(composeProject); err != nil {
		return nil, fmt.Errorf("validate compose project name: %w", err)
	}

	root, err := Root(ctx, cfg, projectRoot)
	if err != nil {
		return nil, err
	}
	arcaveProjectStore, err := ArcaveProject(root, arcaveProject)
	if err != nil {
		return nil, err
	}
	sessionStore, err := Session(arcaveProjectStore, session)
	if err != nil {
		return nil, err
	}
	return ComposeProject(sessionStore, composeProject)
}

// Root opens the store at <path>/arcave and pings it, so a wrong endpoint or a
// refused credential fails here rather than halfway through a capture. A relative
// directory path resolves against projectRoot and is created on first use; an
// absolute one must already exist, so a typo fails.
func Root(ctx context.Context, cfg config.Storage, projectRoot string) (storages.Storager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	store, err := openRoot(ctx, cfg, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("open %s storage: %w", cfg.Type, err)
	}
	if err := store.Ping(ctx); err != nil {
		if closeErr := store.Close(); closeErr != nil {
			log.Ctx(ctx).Warn().Err(closeErr).Str("type", cfg.Type).Msg("close unreachable storage")
		}
		return nil, fmt.Errorf("reach %s storage: %w", cfg.Type, err)
	}
	return store, nil
}

func openRoot(ctx context.Context, cfg config.Storage, projectRoot string) (storages.Storager, error) {
	switch cfg.Type {
	case config.StorageTypeDirectory:
		return openDirectory(*cfg.Directory, projectRoot)
	case config.StorageTypeS3:
		return openS3(ctx, *cfg.S3)
	case config.StorageTypeAzure:
		return openAzure(ctx, *cfg.Azure)
	case config.StorageTypeSSH:
		return openSSH(ctx, *cfg.SSH)
	default:
		// Unreachable: cfg.Validate rejects every other tag.
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Type)
	}
}

func openDirectory(cfg config.DirectoryStorage, projectRoot string) (storages.Storager, error) {
	rootPath, prefix := projectRoot, path.Join(filepath.ToSlash(cfg.Path), storeNamespace)
	if filepath.IsAbs(cfg.Path) {
		rootPath, prefix = cfg.Path, storeNamespace
	}
	return directory.NewStorage(directory.Config{
		RootPath: rootPath,
		Prefix:   prefix,
	}, directory.WithCreatePrefix())
}

func openS3(ctx context.Context, cfg config.S3Storage) (storages.Storager, error) {
	s3Cfg := s3.DefaultConfig()
	s3Cfg.Endpoint = cfg.Endpoint
	s3Cfg.Bucket = cfg.Bucket
	s3Cfg.Prefix = path.Join(cfg.Prefix, storeNamespace)
	s3Cfg.Region = cfg.Region
	if cfg.StorageClass != "" {
		s3Cfg.StorageClass = cfg.StorageClass
	}
	s3Cfg.AccessKeyId = cfg.AccessKeyID
	s3Cfg.SecretAccessKey = cfg.SecretAccessKey
	s3Cfg.SessionToken = cfg.SessionToken
	s3Cfg.RoleArn = cfg.RoleArn
	s3Cfg.SessionName = cfg.SessionName
	if cfg.ForcePathStyle != nil {
		s3Cfg.ForcePathStyle = *cfg.ForcePathStyle
	}
	s3Cfg.DisableSSL = cfg.DisableSSL
	s3Cfg.NoVerifySsl = cfg.NoVerifySSL
	s3Cfg.CertFile = cfg.CertFile
	if cfg.MaxRetries != 0 {
		s3Cfg.MaxRetries = cfg.MaxRetries
	}
	if cfg.MaxPartSize != 0 {
		s3Cfg.MaxPartSize = cfg.MaxPartSize
	}
	s3Cfg.Concurrency = cfg.Concurrency
	s3Cfg.SSE = cfg.SSE
	s3Cfg.KMSKeyARN = cfg.KMSKeyARN
	s3Cfg.BucketKeyEnabled = cfg.BucketKeyEnabled
	return s3.New(ctx, s3Cfg)
}

func openAzure(ctx context.Context, cfg config.AzureStorage) (storages.Storager, error) {
	azureCfg := azure.DefaultConfig()
	azureCfg.Container = cfg.Container
	azureCfg.StorageAccount = cfg.StorageAccount
	azureCfg.Prefix = path.Join(cfg.Prefix, storeNamespace)
	azureCfg.AccessKey = cfg.AccessKey
	azureCfg.SASToken = cfg.SASToken
	azureCfg.Endpoint = cfg.Endpoint
	azureCfg.EndpointSuffix = cfg.EndpointSuffix
	if cfg.EnvironmentName != "" {
		azureCfg.EnvironmentName = cfg.EnvironmentName
	}
	if cfg.MaxRetries != 0 {
		azureCfg.MaxRetries = cfg.MaxRetries
	}
	// azure.New does not validate its config itself.
	if err := azureCfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate azure config: %w", err)
	}
	return azure.New(ctx, azureCfg)
}

func openSSH(ctx context.Context, cfg config.SSHStorage) (storages.Storager, error) {
	sshCfg := ssh.DefaultConfig()
	sshCfg.Host = cfg.Host
	if cfg.Port != 0 {
		sshCfg.Port = cfg.Port
	}
	sshCfg.User = cfg.User
	sshCfg.Password = cfg.Password
	sshCfg.PrivateKeyPath = cfg.PrivateKeyPath
	sshCfg.PrivateKeyPassphrase = cfg.PrivateKeyPassphrase
	sshCfg.Prefix = path.Join(cfg.Prefix, storeNamespace)
	if cfg.ConnectTimeout != 0 {
		sshCfg.ConnectTimeout = cfg.ConnectTimeout
	}
	// ssh.New does not validate its config itself.
	if err := sshCfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate ssh config: %w", err)
	}
	// Once per process: arcave mcp opens the store on every tool call.
	warnHostKeyOnce.Do(func() {
		log.Ctx(ctx).Warn().Str("host", cfg.Host).Msg("ssh storage does not verify the server's host key")
	})
	return ssh.New(sshCfg)
}

// ArcaveProject descends from the root store to projects/<name>.
func ArcaveProject(root storages.Navigator, name model.ArcaveProjectName) (storages.Storager, error) {
	if err := name.Validate(); err != nil {
		return nil, fmt.Errorf("validate arcave project name: %w", err)
	}
	return joinKind(root, projectsNamespace, name.String())
}

// Session descends from an arcave project store to sessions/<id>.
func Session(arcaveProjectStore storages.Navigator, id model.SessionID) (storages.Storager, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("validate session id: %w", err)
	}
	return joinKind(arcaveProjectStore, sessionsNamespace, id.String())
}

// ComposeProject descends from a session store to compose/<name>.
func ComposeProject(sessionStore storages.Navigator, name model.DockerComposeProjectName) (storages.Storager, error) {
	if err := validateComposeProjectName(name); err != nil {
		return nil, fmt.Errorf("validate compose project name: %w", err)
	}
	return joinKind(sessionStore, composeNamespace, name.String())
}

// ComposeProjects lists the compose projects a session holds, sorted by name. A
// directory whose name the compose gate refuses is not one and is skipped.
func ComposeProjects(ctx context.Context, sessionStore storages.Navigator) ([]model.DockerComposeProjectName, error) {
	composeStore, err := sessionStore.SubStorage(composeNamespace, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", composeNamespace, err)
	}
	_, dirs, err := composeStore.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list compose projects: %w", err)
	}

	names := make([]model.DockerComposeProjectName, 0, len(dirs))
	for _, dir := range dirs {
		name := model.DockerComposeProjectName(dir.Dirname())
		if err := validateComposeProjectName(name); err != nil {
			log.Ctx(ctx).Debug().Str("entry", name.String()).Msg("ignoring directory that is not a compose project")
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// joinKind re-roots store at <kind>/<name>; the caller has validated name.
func joinKind(store storages.Navigator, kind, name string) (storages.Storager, error) {
	key := path.Join(kind, name)
	sub, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", key, err)
	}
	return sub, nil
}

// validateComposeProjectName accepts a name compose itself would produce — one its own
// normalization leaves unchanged — rendering as a single path segment. The charset is
// compose's to own; the segment rule is the store's.
func validateComposeProjectName(name model.DockerComposeProjectName) error {
	raw := name.String()
	if raw == "" {
		return fmt.Errorf("invalid compose project name: must not be empty")
	}
	if normalized := loader.NormalizeProjectName(raw); normalized != raw {
		return fmt.Errorf("invalid compose project name %q: compose would name it %q", raw, normalized)
	}
	if path.Base(raw) != raw || raw == "." || raw == ".." {
		return fmt.Errorf("invalid compose project name %q: must be a single path segment", raw)
	}
	return nil
}
