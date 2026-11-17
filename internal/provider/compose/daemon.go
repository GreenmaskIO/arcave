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

package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/go-connections/tlsconfig"
	"github.com/moby/moby/client"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
)

// daemonConnection is one connection to the daemon, with no request sent over it yet.
type daemonConnection struct {
	// cli is what compose runs on, over api, so compose and the engine reach one daemon.
	cli *command.DockerCli
	api client.APIClient
}

// connectDaemon builds the connection; its closer goes on opened.
func connectDaemon(cfg composeconfig.Docker, progressOut io.Writer, opened *closers) (daemonConnection, error) {
	daemon, err := newDockerClient(cfg, progressOut)
	if err != nil {
		return daemonConnection{}, err
	}
	opened.add("docker client", daemon.api)
	return daemon, nil
}

// newDockerClient resolves cfg into the docker/cli instance compose runs on and its API client.
// progressOut becomes the CLI's error stream once the endpoint resolves.
func newDockerClient(cfg composeconfig.Docker, progressOut io.Writer) (daemonConnection, error) {
	clientOpts, err := newDockerClientOptions(cfg)
	if err != nil {
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}

	// DOCKER_API_VERSION is the only seam docker/cli leaves for pinning the API
	// version. Safe to set in-process: arcave spawns no children, so this configures
	// arcave's own client and nothing else.
	if cfg.APIVersion != "" {
		if err := os.Setenv(client.EnvOverrideAPIVersion, cfg.APIVersion); err != nil {
			return daemonConnection{}, fmt.Errorf("pin docker api version %q: %w", cfg.APIVersion, err)
		}
	}

	// stdout is discarded — arcave's own output contract is JSON there. stderr is a
	// buffer for now: it is the only channel docker/cli reports a failed endpoint
	// resolution on, and the buffer turns that report into an error below.
	var initErr bytes.Buffer
	dockerCli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(&initErr),
	)
	if err != nil {
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}
	if err := dockerCli.Initialize(clientOpts); err != nil {
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}

	// Initialize can log a benign warning of its own (a malformed
	// ~/.docker/config.json), so from here on the buffer holds only the resolution.
	initErr.Reset()

	// Resolve the endpoint now, while a failure is still arcave's to report: Client() runs the same
	// sync.Once but prints the cause and exits.
	dockerCli.DockerEndpoint()
	if initErr.Len() > 0 {
		return daemonConnection{}, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %s",
			cfg.Host, cfg.Context, strings.TrimSpace(initErr.String()))
	}
	apiClient := dockerCli.Client()

	// Resolution succeeded, so the error stream becomes compose's progress channel.
	if err := command.WithErrorStream(progressOut)(dockerCli); err != nil {
		closeQuietly(context.Background(), "docker client", apiClient)
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}
	return daemonConnection{
		cli: dockerCli,
		api: apiClient,
	}, nil
}

func newDockerClientOptions(cfg composeconfig.Docker) (*flags.ClientOptions, error) {
	if cfg.Host != "" && cfg.Context != "" {
		return nil, fmt.Errorf("docker.host and docker.context are mutually exclusive: %q already names a daemon, so remove one of the two", cfg.Context)
	}

	opts := flags.NewClientOptions()
	if cfg.Host != "" {
		opts.Hosts = []string{cfg.Host}
	}
	opts.Context = cfg.Context

	resolved, err := withDockerTLSEnv(cfg)
	if err != nil {
		return nil, err
	}
	tlsOpts, useTLS, err := newDockerTLSOptions(resolved)
	if err != nil {
		return nil, err
	}
	if useTLS {
		opts.TLS = true
		opts.TLSVerify = !resolved.SkipTLSVerify
		opts.TLSOptions = tlsOpts
	}
	return opts, nil
}

// withDockerTLSEnv takes the cert path from DOCKER_CERT_PATH under DOCKER_TLS_VERIFY, only beside
// a tcp DOCKER_HOST the config does not override and with no TLS file named in the config. The
// variable with no certificate to verify against is refused: it would stay plain http.
func withDockerTLSEnv(cfg composeconfig.Docker) (composeconfig.Docker, error) {
	if cfg.Host != "" || cfg.Context != "" || !isTCPHost(os.Getenv(client.EnvOverrideHost)) {
		return cfg, nil
	}
	if os.Getenv(client.EnvTLSVerify) == "" || cfg.CertPath != "" || cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" {
		return cfg, nil
	}
	certPath := os.Getenv(client.EnvOverrideCertPath)
	if certPath == "" && !cfg.SkipTLSVerify {
		return composeconfig.Docker{}, errors.New(client.EnvTLSVerify + " is set but " + client.EnvOverrideCertPath + " is not, so the daemon would be reached over plain http: set it, or docker.ca_file or docker.cert_path")
	}
	cfg.CertPath = certPath
	return cfg, nil
}

// isTCPHost reports whether docker/cli reads host as tcp, as it does one with no scheme. One it
// cannot parse is no tcp host: docker/cli refuses it itself.
func isTCPHost(host string) bool {
	parsed, err := dockeropts.ParseHost(false, host)
	return err == nil && strings.HasPrefix(parsed, "tcp://")
}

// newDockerTLSOptions resolves the docker.* TLS fields into docker/cli's TLSOptions and reports
// whether any TLS material was configured. A half-set cert_file/key_file pair is refused here:
// docker/cli would surface it only as an opaque handshake failure.
func newDockerTLSOptions(cfg composeconfig.Docker) (*tlsconfig.Options, bool, error) {
	caFile := tlsFile(cfg.CAFile, cfg.CertPath, "ca.pem")
	certFile := tlsFile(cfg.CertFile, cfg.CertPath, "cert.pem")
	keyFile := tlsFile(cfg.KeyFile, cfg.CertPath, "key.pem")

	switch {
	case certFile != "" && keyFile == "":
		return nil, false, fmt.Errorf("docker.cert_file is set but docker.key_file is missing: a client certificate needs both, or set docker.cert_path to supply the pair from one directory")
	case keyFile != "" && certFile == "":
		return nil, false, fmt.Errorf("docker.key_file is set but docker.cert_file is missing: a client certificate needs both, or set docker.cert_path to supply the pair from one directory")
	}

	// keyFile is not tested: the switch above leaves the pair either both set or both empty.
	if caFile == "" && certFile == "" && !cfg.SkipTLSVerify {
		return nil, false, nil
	}

	return &tlsconfig.Options{
		CAFile:             caFile,
		CertFile:           certFile,
		KeyFile:            keyFile,
		InsecureSkipVerify: cfg.SkipTLSVerify,
	}, true, nil
}

// tlsFile resolves one TLS file path the way the docker CLI does: an explicit
// path wins, then CertPath/name; neither set leaves it empty.
func tlsFile(explicit, certPath, name string) string {
	if explicit != "" {
		return explicit
	}
	if certPath != "" {
		return filepath.Join(certPath, name)
	}
	return ""
}
