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

package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

// connRepoID is the repository a served stream is bound to.
const connRepoID = "volume-named-data"

// errCancelled is the cause a test cancels a served stream with.
var errCancelled = errors.New("cancelled by the test")

// newPipeClient is an HTTP/2 client over one end of a pipe, dialled the way restic
// dials its rclone child: prior knowledge, one connection, never dialled again.
func newPipeClient(conn net.Conn) *http.Client {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	dialed := false
	return &http.Client{Transport: &http.Transport{
		Protocols: &protocols,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if dialed {
				return nil, errors.New("dialled twice")
			}
			dialed = true
			return conn, nil
		},
	}}
}

// serveOverPipe serves connRepoID over a pipe and returns the client end's
// client and the directory the repositories live in.
func serveOverPipe(t *testing.T, ctx context.Context) (*http.Client, string, <-chan error) {
	t.Helper()
	root := t.TempDir()
	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	serverEnd, clientEnd := net.Pipe()
	server := NewConnServer(ctx, repo.NewResolver(store))
	served := make(chan error, 1)
	go func() { served <- server.ServeRepository(ctx, connRepoID, serverEnd) }()
	return newPipeClient(clientEnd), root, served
}

// call sends one request to the served repository and returns its status and body.
func call(t *testing.T, client *http.Client, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/vnd.x.restic.rest.v2")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { assert.NoError(t, resp.Body.Close()) }()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, got
}

// One stream serves the whole of a restic session against one repository, its
// paths taken relative to it.
func TestConnServer_ServeRepository(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	client, root, served := serveOverPipe(t, ctx)
	blob := "/data/" + string(testID)

	// Act and Assert: the steps of a session, in order.
	steps := []struct {
		name   string
		method string
		path   string
		body   []byte
		status int
		want   []byte
	}{
		{name: "the startup probe matches no route", method: http.MethodGet, path: "/file-probe", status: http.StatusNotFound},
		{name: "create the repository", method: http.MethodPost, path: "/?create=true", status: http.StatusOK},
		{name: "save the config", method: http.MethodPost, path: "/config", body: stubBody, status: http.StatusOK},
		{name: "stat the config", method: http.MethodHead, path: "/config", status: http.StatusOK},
		{name: "read the config", method: http.MethodGet, path: "/config", status: http.StatusOK, want: stubBody},
		{name: "save a blob", method: http.MethodPost, path: blob, body: stubBody, status: http.StatusOK},
		{name: "read the blob", method: http.MethodGet, path: blob, status: http.StatusOK, want: stubBody},
		{name: "delete the blob", method: http.MethodDelete, path: blob, status: http.StatusOK},
		{name: "the deleted blob is gone", method: http.MethodHead, path: blob, status: http.StatusNotFound},
	}
	for _, step := range steps {
		status, body := call(t, client, step.method, step.path, step.body)
		assert.Equal(t, step.status, status, step.name)
		if step.want != nil {
			assert.Equal(t, step.want, body, step.name)
		}
	}

	_, err := os.Stat(filepath.Join(root, connRepoID, "config"))
	assert.NoError(t, err, "the config lands under the stream's repository")
	cancel(errCancelled)
	assert.ErrorIs(t, <-served, errCancelled)
}

// A range read answers the slice asked for, over the same stream.
func TestConnServer_ServeRepository_Range(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, _, _ := serveOverPipe(t, ctx)
	blob := "/data/" + string(testID)
	status, _ := call(t, client, http.MethodPost, "/?create=true", nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = call(t, client, http.MethodPost, blob, stubBody)
	require.Equal(t, http.StatusOK, status)
	req, err := http.NewRequest(http.MethodGet, "http://localhost"+blob, nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=2-5")

	// Act
	resp, err := client.Do(req)

	// Assert
	require.NoError(t, err)
	defer func() { assert.NoError(t, resp.Body.Close()) }()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, stubBody[2:6], got)
}

// A client hanging up ends the serve with no error.
func TestConnServer_ServeRepository_ClientHangsUp(t *testing.T) {
	// Arrange
	serverEnd, clientEnd := net.Pipe()
	server := NewConnServer(context.Background(), oneRepository(stubRepository{}))
	served := make(chan error, 1)
	go func() { served <- server.ServeRepository(context.Background(), objectIDName, serverEnd) }()
	client := newPipeClient(clientEnd)
	status, _ := call(t, client, http.MethodGet, "/config", nil)
	require.Equal(t, http.StatusOK, status)

	// Act
	require.NoError(t, clientEnd.Close())

	// Assert
	assert.NoError(t, <-served)
}

// A repository the resolver refuses is never served, and its stream is closed.
func TestConnServer_ServeRepository_Refused(t *testing.T) {
	// Arrange
	serverEnd, clientEnd := net.Pipe()
	server := NewConnServer(context.Background(), oneRepository(stubRepository{}))

	// Act
	err := server.ServeRepository(context.Background(), unknownRepoName, serverEnd)

	// Assert
	require.ErrorIs(t, err, model.ErrRepositoryNotFound)
	_, err = clientEnd.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "the stream is closed")
}
