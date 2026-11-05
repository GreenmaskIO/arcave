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

// Package podman reaches a Podman daemon where its Docker API falls short: libpod's
// own API, and the engine and the API of package docker over it.
package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// libpodHost is the host a request names when the client's transport dials a
// socket of its own: the URL needs one, the dialer ignores it.
const libpodHost = "docker"

// maxLibpodErrorBytes bounds how much of a refused request's body its error carries.
const maxLibpodErrorBytes = 4 << 10

// libpodClient is what reaching Podman's own API past the compat one takes; the
// SDK's client.APIClient satisfies it.
type libpodClient interface {
	HTTPClient() *http.Client
	DaemonHost() string
	ClientVersion() string
}

// Libpod is Podman's own API, reached over the SDK client's connection. A tcp host is
// reached plain: Podman's API service terminates no TLS.
type Libpod struct {
	client libpodClient
}

// NewLibpod wraps client, connected to a Podman daemon.
func NewLibpod(client libpodClient) Libpod {
	return Libpod{client: client}
}

// inspectContainer reads one container as libpod stores it.
func (l Libpod) inspectContainer(ctx context.Context, containerID string) (libpodContainer, error) {
	resp, err := l.call(ctx, http.MethodGet, nil, "containers", containerID, "json")
	if err != nil {
		return libpodContainer{}, err
	}
	return readLibpodJSON[libpodContainer](resp)
}

// commitContainer commits a container's writable layer and returns the image's id as
// libpod answers it, a bare hex.
func (l Libpod) commitContainer(ctx context.Context, commit libpodCommit) (string, error) {
	query := url.Values{
		"container": {commit.Container},
		"format":    {commit.Format},
		"pause":     {strconv.FormatBool(commit.Pause)},
		"changes":   commit.Changes,
	}
	resp, err := l.call(ctx, http.MethodPost, query, "commit")
	if err != nil {
		return "", err
	}
	committed, err := readLibpodJSON[libpodCommitted](resp)
	if err != nil {
		return "", err
	}
	return committed.ID, nil
}

// exportImage streams one image out as an archive of format. The caller owns the
// stream and closes it.
func (l Libpod) exportImage(ctx context.Context, imageID, format string) (io.ReadCloser, error) {
	resp, err := l.call(ctx, http.MethodGet, url.Values{"format": {format}}, "images", imageID, "get")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// call sends one request to libpod's own API, past the compat one; an answer other
// than 2xx is an error carrying its body.
func (l Libpod) call(
	ctx context.Context, method string, query url.Values, path ...string,
) (*http.Response, error) {
	base, err := newLibpodBaseURL(l)
	if err != nil {
		return nil, err
	}
	endpoint := base.JoinPath(append([]string{"v" + l.client.ClientVersion(), "libpod"}, path...)...)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		//nolint:errcheck // the refusal is the error reported
		defer resp.Body.Close()
		//nolint:errcheck // a body cut short still says what it can
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxLibpodErrorBytes))
		return nil, fmt.Errorf("libpod answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// libpodContainer is the part of libpod's container inspect compat leaves out.
type libpodContainer struct {
	Mounts     []libpodMount    `json:"Mounts"`
	HostConfig libpodHostConfig `json:"HostConfig"`
}

// libpodMount is one mount as libpod reports it.
type libpodMount struct {
	Type        string   `json:"Type"`
	Name        string   `json:"Name"`
	Source      string   `json:"Source"`
	Destination string   `json:"Destination"`
	Options     []string `json:"Options"`
	RW          bool     `json:"RW"`
	Propagation string   `json:"Propagation"`
	SubPath     string   `json:"SubPath"`
}

// libpodHostConfig is the part of a container's host config libpod alone reports.
type libpodHostConfig struct {
	LogConfig libpodLogConfig `json:"LogConfig"`
}

// libpodLogConfig is a container's log as libpod reports it.
type libpodLogConfig struct {
	// Size is the log's maximum size; libpod reports none as a negative one.
	Size string `json:"Size"`
}

// libpodCommit is what a commit through libpod is asked for.
type libpodCommit struct {
	Container string
	// Format is the image format: oci or docker.
	Format string
	Pause  bool
	// Changes are Dockerfile instructions applied to the image.
	Changes []string
}

// libpodCommitted is libpod's answer to a commit.
type libpodCommitted struct {
	ID string `json:"Id"`
}

// readLibpodJSON decodes resp's body, and closes it.
func readLibpodJSON[T any](resp *http.Response) (T, error) {
	//nolint:errcheck // the body is read whole below
	defer resp.Body.Close()
	var answer T
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		var zero T
		return zero, fmt.Errorf("decode libpod's answer: %w", err)
	}
	return answer, nil
}

// newLibpodBaseURL is the URL the client's own requests go to: a tcp host as it is,
// any other a placeholder the transport's dialer ignores.
func newLibpodBaseURL(libpod Libpod) (*url.URL, error) {
	host := libpod.client.DaemonHost()
	daemon, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse daemon host %q: %w", host, err)
	}
	if daemon.Scheme != "tcp" {
		return &url.URL{Scheme: "http", Host: libpodHost}, nil
	}
	return &url.URL{Scheme: "http", Host: daemon.Host}, nil
}
