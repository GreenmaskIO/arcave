package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Options is how GM reaches the Docker daemon. It is honoured by *both* halves
// of this package — the SDK client behind Engine, and the docker CLI behind
// ComposeRunner and Engine.wipeMount — because a configuration that only one
// half obeyed would have the two silently targeting different daemons: a
// savepoint exported from daemon A and restored into daemon B.
//
// The zero Options means "read the environment" (DOCKER_HOST and friends), which
// is the behaviour GM had before the config grew a docker section.
type Options struct {
	Host       string // e.g. "unix:///var/run/docker.sock"; "" => from the environment
	APIVersion string // pin the API version; "" => negotiate on the first call
	Context    string // docker CLI context name; requires Host, see NewEngine
	CertPath   string // directory holding ca.pem/cert.pem/key.pem
	TLSVerify  bool   // verify the daemon's certificate
}

// env renders the options as the environment variables the docker CLI reads,
// appended to the process environment so the user's own settings (credentials,
// buildkit) survive. Only the options that are set are exported: an empty Host
// must not clobber an inherited DOCKER_HOST with "".
func (o Options) env() []string {
	env := os.Environ()
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("DOCKER_HOST", o.Host)
	add("DOCKER_CONTEXT", o.Context)
	add("DOCKER_CERT_PATH", o.CertPath)
	if o.TLSVerify {
		env = append(env, "DOCKER_TLS_VERIFY=1")
	}
	return env
}

// newRunner returns the exec seam both ComposeRunner and Engine.wipeMount use:
// one docker CLI invocation, carrying opts through the environment, folding the
// combined output into the error so a failure keeps the daemon's own message.
func newRunner(opts Options) func(ctx context.Context, args ...string) error {
	env := opts.env()
	return func(ctx context.Context, args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
		}
		return nil
	}
}
