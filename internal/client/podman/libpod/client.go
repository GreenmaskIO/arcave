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

// Package libpod is a typed client of Podman's own API, as the Docker SDK is of Docker's:
// the transport and the wire format only.
package libpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
)

const (
	// CommitFormatOCI commits an image in the OCI format.
	CommitFormatOCI = "oci"
	// ExportFormatOCIArchive exports an image as an OCI archive.
	ExportFormatOCIArchive = "oci-archive"
)

// placeholderHost is the host a request names when the connection's transport dials a
// socket of its own: the URL needs one, the dialer ignores it.
const placeholderHost = "docker"

// maxErrorBytes bounds how much of a refused request's body its error carries.
const maxErrorBytes = 4 << 10

// errNoVersion refuses a client with no Podman version for its paths.
var errNoVersion = errors.New("no Podman version to name in the path")

// connection is the daemon connection libpod is reached over, the transport the SDK's client is built on.
type connection interface {
	HTTPClient() *http.Client
	DaemonHost() string
}

// Options is how a Client reaches libpod.
type Options struct {
	// Version is the Podman version the requests name: libpod answers in the shape of the
	// version in its path, and its numbering is not the Docker API's.
	Version string
	// UseTLS says the endpoint carries TLS settings, which the connection applies to a
	// tcp host and the SDK does not expose.
	UseTLS bool
}

// Client is Podman's own API, reached over a daemon connection, certificates included.
type Client struct {
	conn    connection
	baseURL *url.URL
}

// NewClient wraps conn, connected to a Podman daemon.
func NewClient(conn connection, options Options) (*Client, error) {
	if options.Version == "" {
		return nil, errNoVersion
	}
	baseURL, err := newBaseURL(conn.DaemonHost(), options.UseTLS)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:    conn,
		baseURL: baseURL.JoinPath("v"+options.Version, "libpod"),
	}, nil
}

// Container is the part of libpod's container inspect compat leaves out.
type Container struct {
	Mounts     []Mount    `json:"Mounts"`
	HostConfig HostConfig `json:"HostConfig"`
}

// Mount is one mount as libpod reports it.
type Mount struct {
	Type        string   `json:"Type"`
	Name        string   `json:"Name"`
	Source      string   `json:"Source"`
	Destination string   `json:"Destination"`
	Options     []string `json:"Options"`
	RW          bool     `json:"RW"`
	Propagation string   `json:"Propagation"`
	SubPath     string   `json:"SubPath"`
}

// HostConfig is the part of a container's host config libpod alone reports.
type HostConfig struct {
	LogConfig LogConfig `json:"LogConfig"`
}

// LogConfig is a container's log as libpod reports it.
type LogConfig struct {
	// Size is the log's maximum size; libpod reports none as a negative one.
	Size string `json:"Size"`
}

// InspectContainer reads one container as libpod stores it.
func (c *Client) InspectContainer(ctx context.Context, containerID string) (Container, error) {
	resp, err := c.call(ctx, http.MethodGet, nil, "containers", containerID, "json")
	if err != nil {
		return Container{}, err
	}
	return readJSON[Container](resp)
}

// CommitOptions is what a commit is asked for.
type CommitOptions struct {
	Container string
	// Format is the image format: oci or docker.
	Format string
	Pause  bool
	// Changes are Dockerfile instructions applied to the image.
	Changes []string
}

// committed is libpod's answer to a commit.
type committed struct {
	ID string `json:"Id"`
}

// CommitContainer commits a container's writable layer and returns the image's id as
// libpod answers it, a bare hex.
func (c *Client) CommitContainer(ctx context.Context, options CommitOptions) (string, error) {
	query := url.Values{
		"container": {options.Container},
		"format":    {options.Format},
		"pause":     {strconv.FormatBool(options.Pause)},
		"changes":   options.Changes,
	}
	resp, err := c.call(ctx, http.MethodPost, query, "commit")
	if err != nil {
		return "", err
	}
	answer, err := readJSON[committed](resp)
	if err != nil {
		return "", err
	}
	return answer.ID, nil
}

// ExportImage streams one image out as an archive of format. The caller owns the
// stream and closes it.
func (c *Client) ExportImage(ctx context.Context, imageID, format string) (io.ReadCloser, error) {
	resp, err := c.call(ctx, http.MethodGet, url.Values{"format": {format}}, "images", imageID, "get")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// call sends one request to libpod; an answer other than 2xx is an error carrying its body,
// a 404 cerrdefs.ErrNotFound as the SDK's own is.
func (c *Client) call(
	ctx context.Context, method string, query url.Values, path ...string,
) (*http.Response, error) {
	endpoint := c.baseURL.JoinPath(path...)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.conn.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		//nolint:errcheck // the refusal is the error reported
		defer resp.Body.Close()
		//nolint:errcheck // a body cut short still says what it can
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		refusal := fmt.Sprintf("libpod answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusNotFound {
			return nil, cerrdefs.ErrNotFound.WithMessage(refusal)
		}
		return nil, errors.New(refusal)
	}
	return resp, nil
}

// readJSON decodes resp's body, and closes it.
func readJSON[T any](resp *http.Response) (T, error) {
	//nolint:errcheck // the body is read whole below
	defer resp.Body.Close()
	var answer T
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		var zero T
		return zero, fmt.Errorf("decode libpod's answer: %w", err)
	}
	return answer, nil
}

// newBaseURL is the URL the connection's own requests go to: a tcp host as it is, over
// https under TLS settings; any other a placeholder the transport's dialer ignores.
func newBaseURL(host string, useTLS bool) (*url.URL, error) {
	daemon, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse daemon host %q: %w", host, err)
	}
	switch {
	case daemon.Scheme != "tcp":
		return &url.URL{
			Scheme: "http",
			Host:   placeholderHost,
		}, nil
	case useTLS:
		return &url.URL{
			Scheme: "https",
			Host:   daemon.Host,
			Path:   daemon.Path,
		}, nil
	default:
		return &url.URL{
			Scheme: "http",
			Host:   daemon.Host,
			Path:   daemon.Path,
		}, nil
	}
}
