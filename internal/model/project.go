package model

// Project is the `project` section of .arcave/config.yaml as `project apply`
// sends it.
type Project struct {
	Name      string    `mapstructure:"name"`
	Providers Providers `mapstructure:"providers"`
}

// Providers names the registered providers a project may use, by id and never
// by type.
type Providers struct {
	// Sandbox is the provider the project's sandbox runs on, if it has one.
	Sandbox string `mapstructure:"sandbox"`
	// Stacks are the providers the project's stacks may live on.
	Stacks []string `mapstructure:"stacks"`
}
