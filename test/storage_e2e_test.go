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

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"
	"github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// The storage servers the e2e runs against, pinned by digest: the images the
// storages library's own integration tests use. Chainguard publishes MinIO only as
// latest, so its digest is the pin; refresh one by pulling and re-reading RepoDigests.
const (
	minioImage   = "cgr.dev/chainguard/minio@sha256:4692462f35d97d7e82c30371d82f057703c5d9489bcae726010594c812f2d285"
	azuriteImage = "mcr.microsoft.com/azure-storage/azurite:3.35.0@sha256:647c63a91102a9d8e8000aab803436e1fc85fbb285e7ce830a82ee5d6661cf37"
	sftpImage    = "atmoz/sftp:alpine@sha256:a6cb3eb29202ca7f57e73bb7e527286e66e0e822fff65609207c7e0ef2d135a3"
)

const (
	storageE2EArcaveProject = "arcave-storage-e2e"
	storageE2EBucket        = "arcave-e2e"
	sftpUser                = "arcave"
	sftpPassword            = "arcave-e2e"
	s3SecretVar             = "E2E_S3_SECRET_ACCESS_KEY"
	azureKeyVar             = "E2E_AZURE_ACCESS_KEY"
	// storageReadyTimeout bounds a server's startup: sftp generates its host keys on boot.
	storageReadyTimeout = 2 * time.Minute
)

// storageTarget is one storage server as arcave sees it: the arm config.yaml carries,
// the ARCAVE_* variables the binary runs with, and the full config the test opens to
// check what landed.
type storageTarget struct {
	file    config.Storage
	env     []string
	storage config.Storage
}

// storageServers start each server the storages library ships a backend for, one
// per subtest, stopped with it.
var storageServers = []struct {
	name  string
	start func(t *testing.T) storageTarget
}{
	{name: config.StorageTypeDirectory, start: startDirectoryStorage},
	{name: config.StorageTypeS3, start: startS3Storage},
	{name: config.StorageTypeAzure, start: startAzureStorage},
	{name: config.StorageTypeSSH, start: startSSHStorage},
}

// TestStorageE2E_CoreWorkflow drives the built binary through the core workflow on
// every storage backend: create, list, rollback, fork and remove.
func TestStorageE2E_CoreWorkflow(t *testing.T) {
	t.Parallel()

	for _, server := range storageServers {
		t.Run(server.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			compose := newImageRollbackEnv(t)
			target := server.start(t)
			projectRoot := t.TempDir()
			run := func(args ...string) []byte {
				t.Helper()
				return runArcave(t, projectRoot, target.env, append([]string{"-p", compose.project}, args...)...)
			}
			runArcave(t, projectRoot, target.env, "init", "--name", storageE2EArcaveProject)
			writeStorageConfig(t, projectRoot, target.file)
			compose.up(t, 1)
			compose.write(t, compose.replicas(t)[0], "v1")

			// Act
			sp1 := decode[model.Savepoint](t, run("savepoint", "create", "sp1"))
			compose.write(t, compose.replicas(t)[0], "v2")
			sp2 := decode[model.Savepoint](t, run("savepoint", "create", "sp2"))
			listed := decode[[]model.Savepoint](t, run("savepoint", "list"))
			ascending := decode[[]model.Savepoint](t, run("savepoint", "list", "--sort", "asc"))
			rolledBack := decode[model.RollbackResponse](t, run("rollback", "sp1"))
			replica := compose.replicas(t)[0]
			volumeAfter, layerAfter := compose.read(t, replica, volumeMarker), compose.read(t, replica, layerMarker)
			withSafety := decode[[]model.Savepoint](t, run("savepoint", "list", "--include-safety-snapshots"))
			fork := decode[model.Session](t, run("session", "create", "--name", "fork", "--from", model.DefaultSessionName))
			inFork := decode[[]model.Savepoint](t, run("--session", "fork", "savepoint", "list"))
			removed := decode[model.RemoveResponse](t, run("savepoint", "remove", sp2.ID.String()))
			afterRemove := decode[[]model.Savepoint](t, run("savepoint", "list"))

			// Assert
			assert.Equal(t, []string{"sp2", "sp1"}, savepointNames(listed), "newest first by default")
			assert.Equal(t, []string{"sp1", "sp2"}, savepointNames(ascending))
			assert.Equal(t, sp1.ID, rolledBack.RolledBackTo.ID)
			assert.Equal(t, "v1", volumeAfter, "the volume is back at sp1")
			assert.Equal(t, "v1", layerAfter, "the writable layer is back at sp1")
			assert.Equal(t, []string{"sp2", "sp1"}, savepointNames(withSafety), "the safety snapshot is dropped")
			assert.Equal(t, []string{"sp2", "sp1"}, savepointNames(inFork), "the fork holds the copied savepoints")
			assert.Equal(t, []model.SavepointID{sp2.ID}, savepointIDs(removed.Removed))
			assert.Equal(t, []string{"sp1"}, savepointNames(afterRemove))

			assertRecord(t, target.storage, compose.project, model.DefaultSession, sp1.ID, true)
			assertRecord(t, target.storage, compose.project, model.DefaultSession, sp2.ID, false)
			assertRecord(t, target.storage, compose.project, fork.ID, sp2.ID, true)
		})
	}
}

func startDirectoryStorage(t *testing.T) storageTarget {
	t.Helper()

	storage := config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: t.TempDir()},
	}
	return storageTarget{file: storage, storage: storage}
}

// startS3Storage runs MinIO with one bucket; config.yaml names the secret by ${VAR}.
func startS3Storage(t *testing.T) storageTarget {
	t.Helper()

	ctx := t.Context()
	server, err := minio.Run(ctx, minioImage,
		// The image's non-root user cannot run the server on the /data the module points it at.
		testcontainers.WithConfigModifier(func(c *dockercontainer.Config) { c.User = "0:0" }),
	)
	testcontainers.CleanupContainer(t, server)
	require.NoError(t, err)
	endpoint, err := server.ConnectionString(ctx)
	require.NoError(t, err)

	bucket := config.S3Storage{
		Endpoint:    "http://" + endpoint,
		Bucket:      storageE2EBucket,
		Prefix:      "it-" + randomSuffix(t),
		Region:      "us-east-1",
		AccessKeyID: server.Username,
	}
	client := awss3.New(awss3.Options{
		Region:       bucket.Region,
		BaseEndpoint: aws.String(bucket.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(server.Username, server.Password, ""),
	})
	_, err = client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket.Bucket)})
	require.NoError(t, err)

	inFile := bucket
	inFile.SecretAccessKey = "${" + s3SecretVar + "}"
	bucket.SecretAccessKey = server.Password
	return storageTarget{
		file:    config.Storage{Type: config.StorageTypeS3, S3: &inFile},
		env:     []string{s3SecretVar + "=" + server.Password},
		storage: config.Storage{Type: config.StorageTypeS3, S3: &bucket},
	}
}

// startAzureStorage runs Azurite's blob service with one container; config.yaml names
// the access key by ${VAR}.
func startAzureStorage(t *testing.T) storageTarget {
	t.Helper()

	ctx := t.Context()
	server, err := azurite.Run(ctx, azuriteImage,
		azurite.WithEnabledServices(azurite.BlobService),
		// Lets the emulator accept the API version the current SDK sends.
		testcontainers.WithCmdArgs("--skipApiVersionCheck"),
	)
	testcontainers.CleanupContainer(t, server)
	require.NoError(t, err)
	serviceURL, err := server.BlobServiceURL(ctx)
	require.NoError(t, err)

	blobs := config.AzureStorage{
		Container:      "arcave-e2e",
		StorageAccount: azurite.AccountName,
		Prefix:         "it-" + randomSuffix(t),
		AccessKey:      azurite.AccountKey,
		Endpoint:       serviceURL + "/" + azurite.AccountName,
	}
	credential, err := container.NewSharedKeyCredential(azurite.AccountName, azurite.AccountKey)
	require.NoError(t, err)
	client, err := container.NewClientWithSharedKeyCredential(blobs.Endpoint+"/"+blobs.Container, credential, nil)
	require.NoError(t, err)
	_, err = client.Create(ctx, nil)
	require.NoError(t, err)

	inFile := blobs
	inFile.AccessKey = "${" + azureKeyVar + "}"
	return storageTarget{
		file:    config.Storage{Type: config.StorageTypeAzure, Azure: &inFile},
		env:     []string{azureKeyVar + "=" + azurite.AccountKey},
		storage: config.Storage{Type: config.StorageTypeAzure, Azure: &blobs},
	}
}

// startSSHStorage runs an SFTP server with one user, chrooted to a home holding a writable /upload.
func startSSHStorage(t *testing.T) storageTarget {
	t.Helper()

	ctx := t.Context()
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        sftpImage,
			ExposedPorts: []string{"22/tcp"},
			Cmd:          []string{sftpUser + ":" + sftpPassword + ":::upload"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("22/tcp"),
				wait.ForLog("Server listening on 0.0.0.0 port 22"),
			).WithDeadline(storageReadyTimeout),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, server)
	require.NoError(t, err)
	endpoint, err := server.PortEndpoint(ctx, "22/tcp", "")
	require.NoError(t, err)
	host, rawPort, err := net.SplitHostPort(endpoint)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)

	storage := config.Storage{Type: config.StorageTypeSSH, SSH: &config.SSHStorage{
		Host:     host,
		Port:     port,
		User:     sftpUser,
		Password: sftpPassword,
		Prefix:   path.Join("/upload", "it-"+randomSuffix(t)),
	}}
	// The port accepts connections before sshd serves on it; a real ping is the signal.
	require.Eventually(t, func() bool {
		root, err := storefactory.Root(ctx, storage, t.TempDir())
		if err != nil {
			return false
		}
		_ = root.Close()
		return true
	}, storageReadyTimeout, 500*time.Millisecond, "sftp server never accepted a session")
	return storageTarget{file: storage, storage: storage}
}

// runArcave runs the binary in projectRoot with env alone of the ARCAVE_* variables,
// failing the test unless it succeeds, and returns its stdout.
func runArcave(t *testing.T, projectRoot string, env []string, args ...string) []byte {
	t.Helper()

	cmd := arcaveCommandIn(t, projectRoot, args...)
	cmd.Env = append(cmd.Env, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "arcave %v: %s", args, stderr.String())
	return out
}

// writeStorageConfig points the project's config.yaml at storage, keeping what init wrote.
func writeStorageConfig(t *testing.T, projectRoot string, storage config.Storage) {
	t.Helper()

	file := config.Path(projectRoot)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	cfg.Storage = storage
	data, err = yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0o600))
}

// assertRecord says whether the store holds id's commit marker in the session's compose project.
func assertRecord(
	t *testing.T, storage config.Storage, composeProject string, session model.SessionID, id model.SavepointID, want bool,
) {
	t.Helper()

	ctx := context.Background()
	root, err := storefactory.Root(ctx, storage, t.TempDir())
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	key := path.Join("projects", storageE2EArcaveProject, "sessions", session.String(),
		"compose", composeProject, id.String(), "savepoint.json")

	exists, err := root.Exists(ctx, key)

	require.NoError(t, err)
	assert.Equal(t, want, exists, "record %s", key)
}

func decode[T any](t *testing.T, out []byte) T {
	t.Helper()

	var value T
	require.NoError(t, json.Unmarshal(out, &value), "decode %s", out)
	return value
}

func savepointNames(savepoints []model.Savepoint) []string {
	names := make([]string, len(savepoints))
	for i, savepoint := range savepoints {
		names[i] = savepoint.Name
	}
	return names
}

func savepointIDs(savepoints []model.Savepoint) []model.SavepointID {
	ids := make([]model.SavepointID, len(savepoints))
	for i, savepoint := range savepoints {
		ids[i] = savepoint.ID
	}
	return ids
}
