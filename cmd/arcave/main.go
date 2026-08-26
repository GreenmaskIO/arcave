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
	"context"
	"os"
)

// main executes the command tree. Config resolution and logger setup happen in
// rootCmd's PersistentPreRunE (cmd_root.go); each command builds the dependencies
// it needs via initAllDependencies (dependencies.go) and releases them on return.
func main() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		// cobra has already reported the error on stderr.
		os.Exit(1)
	}
}
