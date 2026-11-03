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

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/service/storekey"
)

var (
	configInitName     string
	configInitOverride bool
	configInitEncrypt  bool

	configInitCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize arcave metadata for the current project",
		Args:  cobra.NoArgs,
		RunE:  runConfigInit,
	}
)

// runConfigInit writes the config the other commands require: a template of the
// defaults and --name, whatever the environment or an existing file says. It names
// no compose project and opens neither the storage backend nor the Docker engine.
func runConfigInit(cmd *cobra.Command, _ []string) error {
	if configFound && !configInitOverride {
		return fmt.Errorf("project already initialized use --override")
	}

	var (
		keyNotice string
		err       error
	)
	if configInitEncrypt {
		// projectCfg carries the variable: setupViper binds encryption.key to it.
		if keyNotice, err = newInitKeyNotice(projectCfg.Encryption.Key); err != nil {
			return err
		}
	}
	svc, printer := newInitDependencies(projectRoot)
	h := cli.NewConfigInitHandler(svc, printer)
	if err = h.Init(cmd.Context(), newInitTemplate(configInitName, configInitEncrypt), configInitOverride); err != nil {
		return err
	}
	if keyNotice != "" {
		_, err = fmt.Fprint(cmd.ErrOrStderr(), keyNotice)
	}
	return err
}

// newInitTemplate is the config init writes: the defaults, the name, and under encrypt
// encryption on with no key — the key stays in encryptionKeyEnv, never in the file.
func newInitTemplate(name string, encrypt bool) config.Config {
	template := config.NewConfig()
	template.Project.Name = name
	if encrypt {
		template.Encryption = config.Encryption{Enabled: true}
	}
	return template
}

// newInitKeyNotice is what init tells of the store key on stderr: a key it generates while
// envKey is empty, else only a reminder, envKey refused when it is no store key.
func newInitKeyNotice(envKey string) (string, error) {
	if envKey != "" {
		if _, err := storekey.ParseKey(envKey); err != nil {
			return "", fmt.Errorf("%s: %w", encryptionKeyEnv, err)
		}
		return fmt.Sprintf("The store is encrypted to the key in %s: keep it, arcave cannot recover it.\n",
			encryptionKeyEnv), nil
	}
	key, err := storekey.NewKey()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Store key generated: save it, arcave cannot recover it.\n  export %s=%s\n",
		encryptionKeyEnv, key), nil
}

func init() {
	flags := configInitCmd.Flags()
	flags.StringVar(&configInitName, "name", "",
		"Project name (default: current directory name)")
	flags.BoolVar(&configInitOverride, "override", false,
		"Overwrite an existing arcave configuration")
	flags.BoolVar(&configInitEncrypt, "encrypt", false,
		"Encrypt the store to the key read from "+encryptionKeyEnv+", generating one when unset")

	// No flag is bound: the template takes --name and --encrypt directly, and --override is
	// transient. The storage is the directory one at its default path.

	rootCmd.AddCommand(configInitCmd)
}
