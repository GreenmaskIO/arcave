package stack

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/tlsconfig"
)

// initDockerClient resolves cfg into a docker/cli instance and the APIClient it uses.
func initDockerClient(cfg Docker, progressOut io.Writer) (*command.DockerCli, client.APIClient, error) {
	clientOpts, err := getDockerOptions(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// The only way docker/cli takes an API version; safe in-process, arcave spawns no children.
	if cfg.APIVersion != "" {
		if err := os.Setenv(client.EnvOverrideAPIVersion, cfg.APIVersion); err != nil {
			return nil, nil, fmt.Errorf("pin docker api version %q: %w", cfg.APIVersion, err)
		}
	}

	// stderr is a buffer: docker/cli reports a failed endpoint resolution nowhere else.
	var initErr bytes.Buffer
	dockerCli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(&initErr),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}
	if err := dockerCli.Initialize(clientOpts); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// Built here rather than left to dockerCli.Client(), which answers a failure
	// with os.Exit(1); injecting it back keeps compose on this very client.
	apiClient, err := command.NewAPIClientFromFlags(clientOpts, dockerCli.ConfigFile())
	if err != nil {
		return nil, nil, fmt.Errorf("connect to docker engine (connection.host %q, connection.context %q): %w", cfg.Host, cfg.Context, err)
	}
	if err := command.WithAPIClient(apiClient)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// Initialize can log a benign warning of its own; from here the buffer holds
	// only the resolution.
	initErr.Reset()

	// Resolve now, while the failure is still arcave's to report: Client() runs the
	// same sync.Once but prints the cause and exits, and compose calls it itself.
	dockerCli.DockerEndpoint()
	if initErr.Len() > 0 {
		return nil, nil, fmt.Errorf("connect to docker engine (connection.host %q, connection.context %q): %s",
			cfg.Host, cfg.Context, strings.TrimSpace(initErr.String()))
	}

	// Resolution succeeded, so the error stream becomes compose's progress channel.
	if err := command.WithErrorStream(progressOut)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}
	return dockerCli, apiClient, nil
}

func getDockerOptions(cfg Docker) (*flags.ClientOptions, error) {
	if cfg.Host != "" && cfg.Context != "" {
		return nil, fmt.Errorf("connection.host and connection.context are mutually exclusive: %q already names a daemon, so remove one of the two", cfg.Context)
	}

	opts := flags.NewClientOptions()
	if cfg.Host != "" {
		opts.Hosts = []string{cfg.Host}
	}
	opts.Context = cfg.Context

	tlsOpts, useTLS, err := dockerTLSOptions(cfg)
	if err != nil {
		return nil, err
	}
	if useTLS {
		opts.TLS = true
		opts.TLSVerify = cfg.TLSVerify
		opts.TLSOptions = tlsOpts
	}
	return opts, nil
}

// dockerTLSOptions resolves the connection.* TLS fields and reports whether any
// TLS material was configured; all-empty means "read the environment".
func dockerTLSOptions(cfg Docker) (*tlsconfig.Options, bool, error) {
	caFile := tlsFile(cfg.CAFile, cfg.CertPath, "ca.pem")
	certFile := tlsFile(cfg.CertFile, cfg.CertPath, "cert.pem")
	keyFile := tlsFile(cfg.KeyFile, cfg.CertPath, "key.pem")

	switch {
	case certFile != "" && keyFile == "":
		return nil, false, fmt.Errorf("connection.cert_file is set but connection.key_file is missing: a client certificate needs both, or set connection.cert_path to supply the pair from one directory")
	case keyFile != "" && certFile == "":
		return nil, false, fmt.Errorf("connection.key_file is set but connection.cert_file is missing: a client certificate needs both, or set connection.cert_path to supply the pair from one directory")
	}

	// keyFile is not tested: the switch above leaves the pair either both set or both empty.
	if caFile == "" && certFile == "" && !cfg.TLSVerify {
		return nil, false, nil
	}

	return &tlsconfig.Options{
		CAFile:   caFile,
		CertFile: certFile,
		KeyFile:  keyFile,
	}, true, nil
}

// tlsFile resolves one TLS file path: an explicit path wins, then CertPath/name.
func tlsFile(explicit, certPath, name string) string {
	if explicit != "" {
		return explicit
	}
	if certPath != "" {
		return filepath.Join(certPath, name)
	}
	return ""
}
