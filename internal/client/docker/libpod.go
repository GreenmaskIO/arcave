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

package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Libpod reaches Podman's own API over the SDK client's connection. A tcp host is
// reached plain: Podman's API service terminates no TLS.
type Libpod struct {
	client libpodClient
}

// NewLibpod wraps client, connected to a Podman daemon.
func NewLibpod(client libpodClient) Libpod {
	return Libpod{client: client}
}

// callLibpod sends one request to libpod's own API, past the compat one; an answer
// other than 2xx is an error carrying its body.
func callLibpod(
	ctx context.Context, libpod Libpod, method string, query url.Values, path ...string,
) (*http.Response, error) {
	base, err := newLibpodBaseURL(libpod)
	if err != nil {
		return nil, err
	}
	endpoint := base.JoinPath(append([]string{"v" + libpod.client.ClientVersion(), "libpod"}, path...)...)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := libpod.client.HTTPClient().Do(req)
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
