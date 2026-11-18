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

package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// The arms of the storage union, one per storages backend arcave opens.
const (
	// StorageTypeDirectory is a local directory, the default.
	StorageTypeDirectory = "directory"
	// StorageTypeS3 is an S3-compatible bucket.
	StorageTypeS3 = "s3"
	// StorageTypeAzure is an Azure Blob Storage container.
	StorageTypeAzure = "azure"
	// StorageTypeSSH is a directory on an SFTP server.
	StorageTypeSSH = "ssh"
)

// DefaultSSHPort is the port storage.ssh.port defaults to.
const DefaultSSHPort = 22

var storageTypes = []string{StorageTypeDirectory, StorageTypeS3, StorageTypeAzure, StorageTypeSSH}

// Storage is a tagged union: Type names the arm, and exactly that arm must be
// present. The arms are pointers so an unset one is omitted on marshal rather
// than written out as a block of zero values.
type Storage struct {
	Type      string            `yaml:"type" mapstructure:"type"`
	Directory *DirectoryStorage `yaml:"directory,omitempty" mapstructure:"directory"`
	S3        *S3Storage        `yaml:"s3,omitempty" mapstructure:"s3"`
	Azure     *AzureStorage     `yaml:"azure,omitempty" mapstructure:"azure"`
	SSH       *SSHStorage       `yaml:"ssh,omitempty" mapstructure:"ssh"`
}

// DirectoryStorage is the local-filesystem arm. Path is resolved against the
// project root when relative, and rooted under the static arcave namespace, by
// internal/storefactory — which is the only place that turns it into a Storager.
type DirectoryStorage struct {
	Path string `yaml:"path" mapstructure:"path"`
}

// S3Storage is the S3 arm. Static keys are optional: left empty, the AWS default
// credential chain applies. ForcePathStyle defaults to true, as MinIO needs.
type S3Storage struct {
	Endpoint         string `yaml:"endpoint,omitempty" mapstructure:"endpoint"`
	Bucket           string `yaml:"bucket" mapstructure:"bucket"`
	Prefix           string `yaml:"prefix,omitempty" mapstructure:"prefix"`
	Region           string `yaml:"region,omitempty" mapstructure:"region"`
	StorageClass     string `yaml:"storage_class,omitempty" mapstructure:"storage_class"`
	AccessKeyID      string `yaml:"access_key_id,omitempty" mapstructure:"access_key_id"`
	SecretAccessKey  string `yaml:"secret_access_key,omitempty" mapstructure:"secret_access_key"`
	SessionToken     string `yaml:"session_token,omitempty" mapstructure:"session_token"`
	RoleArn          string `yaml:"role_arn,omitempty" mapstructure:"role_arn"`
	SessionName      string `yaml:"session_name,omitempty" mapstructure:"session_name"`
	ForcePathStyle   *bool  `yaml:"force_path_style,omitempty" mapstructure:"force_path_style"`
	DisableSSL       bool   `yaml:"disable_ssl,omitempty" mapstructure:"disable_ssl"`
	NoVerifySSL      bool   `yaml:"no_verify_ssl,omitempty" mapstructure:"no_verify_ssl"`
	CertFile         string `yaml:"cert_file,omitempty" mapstructure:"cert_file"`
	MaxRetries       int    `yaml:"max_retries,omitempty" mapstructure:"max_retries"`
	MaxPartSize      int64  `yaml:"max_part_size,omitempty" mapstructure:"max_part_size"`
	Concurrency      int    `yaml:"concurrency,omitempty" mapstructure:"concurrency"`
	SSE              string `yaml:"sse,omitempty" mapstructure:"sse"`
	KMSKeyARN        string `yaml:"kms_key_arn,omitempty" mapstructure:"kms_key_arn"`
	BucketKeyEnabled bool   `yaml:"bucket_key_enabled,omitempty" mapstructure:"bucket_key_enabled"`
}

// AzureStorage is the Azure Blob Storage arm. With neither AccessKey nor SASToken
// set, DefaultAzureCredential applies. Endpoint overrides the service URL (Azurite).
type AzureStorage struct {
	Container       string `yaml:"container" mapstructure:"container"`
	StorageAccount  string `yaml:"storage_account" mapstructure:"storage_account"`
	Prefix          string `yaml:"prefix,omitempty" mapstructure:"prefix"`
	AccessKey       string `yaml:"access_key,omitempty" mapstructure:"access_key"`
	SASToken        string `yaml:"sas_token,omitempty" mapstructure:"sas_token"`
	Endpoint        string `yaml:"endpoint,omitempty" mapstructure:"endpoint"`
	EndpointSuffix  string `yaml:"endpoint_suffix,omitempty" mapstructure:"endpoint_suffix"`
	EnvironmentName string `yaml:"environment_name,omitempty" mapstructure:"environment_name"`
	MaxRetries      int    `yaml:"max_retries,omitempty" mapstructure:"max_retries"`
}

// SSHStorage is the SFTP arm: Password or PrivateKeyPath authenticates, an encrypted
// key with PrivateKeyPassphrase. ConnectTimeout bounds the dial and the handshake,
// the library's 30s when zero. The server's host key is not verified.
type SSHStorage struct {
	Host                 string        `yaml:"host" mapstructure:"host"`
	Port                 int           `yaml:"port,omitempty" mapstructure:"port"`
	User                 string        `yaml:"user" mapstructure:"user"`
	Password             string        `yaml:"password,omitempty" mapstructure:"password"`
	PrivateKeyPath       string        `yaml:"private_key_path,omitempty" mapstructure:"private_key_path"`
	PrivateKeyPassphrase string        `yaml:"private_key_passphrase,omitempty" mapstructure:"private_key_passphrase"`
	Prefix               string        `yaml:"prefix,omitempty" mapstructure:"prefix"`
	ConnectTimeout       time.Duration `yaml:"connect_timeout,omitempty" mapstructure:"connect_timeout"`
}

// Validate rejects a union whose tag names an arm that is not present, an arm
// missing a required field, or an arm arcave does not implement.
func (s Storage) Validate() error {
	switch s.Type {
	case StorageTypeDirectory:
		if s.Directory == nil {
			return missingArmError(s.Type)
		}
		return requireFields(s.Type, [2]string{"path", s.Directory.Path})
	case StorageTypeS3:
		if s.S3 == nil {
			return missingArmError(s.Type)
		}
		if err := requireFields(s.Type, [2]string{"bucket", s.S3.Bucket}); err != nil {
			return err
		}
		return validateEndpoint(s.Type, s.S3.Endpoint, "")
	case StorageTypeAzure:
		if s.Azure == nil {
			return missingArmError(s.Type)
		}
		if err := requireFields(s.Type,
			[2]string{"container", s.Azure.Container}, [2]string{"storage_account", s.Azure.StorageAccount}); err != nil {
			return err
		}
		return validateEndpoint(s.Type, s.Azure.Endpoint, "put a SAS token in storage.azure.sas_token")
	case StorageTypeSSH:
		if s.SSH == nil {
			return missingArmError(s.Type)
		}
		if err := requireFields(s.Type, [2]string{"host", s.SSH.Host}, [2]string{"user", s.SSH.User}); err != nil {
			return err
		}
		if s.SSH.Password == "" && s.SSH.PrivateKeyPath == "" {
			return fmt.Errorf("%w: storage.ssh needs password or private_key_path", ErrInvalid)
		}
		if s.SSH.PrivateKeyPassphrase != "" && s.SSH.PrivateKeyPath == "" {
			return fmt.Errorf("%w: storage.ssh.private_key_passphrase needs private_key_path", ErrInvalid)
		}
		if s.SSH.ConnectTimeout < 0 {
			return fmt.Errorf("%w: storage.ssh.connect_timeout must not be negative, got %s", ErrInvalid, s.SSH.ConnectTimeout)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported storage.type %q (supported: %s)", ErrInvalid,
			s.Type, strings.Join(storageTypes, ", "))
	}
}

// DirectoryPath returns the directory arm's path, or "" when the union does not
// name that arm. Nothing turns the path into a store this way — that is
// storefactory's alone.
func (s Storage) DirectoryPath() string {
	if s.Directory == nil {
		return ""
	}
	return s.Directory.Path
}

// withDefaults fills the selected arm's defaults and drops every other arm. Each
// arm is filled on a copy, never through the caller's pointer, since a Config is
// passed around by value.
func (s Storage) withDefaults() Storage {
	if s.Type == "" {
		s.Type = StorageTypeDirectory
	}
	selected := Storage{Type: s.Type}
	switch s.Type {
	case StorageTypeDirectory:
		dir := utils.Value(s.Directory)
		if dir.Path == "" {
			// No project segment here: storefactory roots every store at
			// arcave/projects/<project>/…, so naming it twice would nest it.
			dir.Path = filepath.Join(model.StateDir, "storage")
		}
		selected.Directory = &dir
	case StorageTypeS3:
		if s.S3 != nil {
			bucket := *s.S3
			if bucket.ForcePathStyle == nil {
				bucket.ForcePathStyle = utils.New(true)
			}
			selected.S3 = &bucket
		}
	case StorageTypeAzure:
		if s.Azure != nil {
			selected.Azure = utils.New(*s.Azure)
		}
	case StorageTypeSSH:
		if s.SSH != nil {
			server := *s.SSH
			if server.Port == 0 {
				server.Port = DefaultSSHPort
			}
			selected.SSH = &server
		}
	default:
		// An unknown type keeps its arms out of the way of Validate's message.
	}
	return selected
}

func missingArmError(storageType string) error {
	return fmt.Errorf("%w: storage.type is %q but no storage.%s section is present", ErrInvalid, storageType, storageType)
}

// validateEndpoint refuses an endpoint URL carrying a credential: user info, or a
// query string — where a SAS token pasted with its URL lands. hint says where it goes.
func validateEndpoint(storageType, endpoint, hint string) error {
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%w: storage.%s.endpoint: %w", ErrInvalid, storageType, err)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: storage.%s.endpoint must not carry user info; set the credential fields instead",
			ErrInvalid, storageType)
	}
	if parsed.RawQuery != "" {
		if hint != "" {
			hint = "; " + hint
		}
		return fmt.Errorf("%w: storage.%s.endpoint must not carry a query string%s", ErrInvalid, storageType, hint)
	}
	return nil
}

// requireFields reports the first of the named fields that is empty.
func requireFields(storageType string, fields ...[2]string) error {
	for _, field := range fields {
		if field[1] == "" {
			return fmt.Errorf("%w: storage.%s.%s is required", ErrInvalid, storageType, field[0])
		}
	}
	return nil
}
