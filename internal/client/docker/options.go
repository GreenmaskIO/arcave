package docker

import (
	"fmt"
	"path/filepath"

	"github.com/docker/cli/cli/flags"
	"github.com/docker/go-connections/tlsconfig"
)

// Options is how GM reaches the Docker daemon. One Engine translates it once,
// into the docker/cli client options both halves of this package then share —
// the API client behind Engine's volume I/O and the compose library behind
// ComposeRunner both come from that single resolved client, so a configuration
// that only one half obeyed (the failure the exec-based version had to guard
// against by hand) is no longer expressible.
//
// The zero Options means "read the environment" (DOCKER_HOST and friends), which
// is the behaviour GM had before the config grew a docker section.
type Options struct {
	Host       string // e.g. "unix:///var/run/docker.sock"; "" => from the environment
	APIVersion string // pin the API version; "" => negotiate on the first call
	Context    string // docker CLI context name; mutually exclusive with Host
	CertPath   string // directory holding ca.pem/cert.pem/key.pem
	TLSVerify  bool   // verify the daemon's certificate
}

// clientOptions renders the options as docker/cli's own client options, the form
// command.DockerCli.Initialize takes. Only what is set is passed through: an
// empty field must defer to the environment, not override it with "".
//
// Host and Context are mutually exclusive — docker/cli refuses the pair, since a
// context already names a host and there is no sensible winner. That is the
// inverse of the rule this package used to carry ("a context needs a host"),
// which existed only because the raw SDK client could not resolve a context at
// all; docker/cli's context store can, so a bare Context is now the supported
// way to name a remote daemon.
func (o Options) clientOptions() (*flags.ClientOptions, error) {
	if o.Host != "" && o.Context != "" {
		return nil, fmt.Errorf("docker.host and docker.context are mutually exclusive: %q already names a daemon, so remove one of the two", o.Context)
	}

	opts := flags.NewClientOptions()
	if o.Host != "" {
		opts.Hosts = []string{o.Host}
	}
	opts.Context = o.Context
	if o.CertPath != "" || o.TLSVerify {
		opts.TLSVerify = o.TLSVerify
		opts.TLS = true
		opts.TLSOptions = &tlsconfig.Options{
			CAFile:   filepath.Join(o.CertPath, "ca.pem"),
			CertFile: filepath.Join(o.CertPath, "cert.pem"),
			KeyFile:  filepath.Join(o.CertPath, "key.pem"),
		}
	}
	return opts, nil
}
