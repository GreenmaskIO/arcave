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
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
)

// exitInterrupted is the shell's code for a process ended by SIGINT.
const exitInterrupted = 130

// main executes the command tree. Config resolution and logger setup happen in
// rootCmd's PersistentPreRunE (cmd_root.go); each command builds the dependencies
// it needs via initAllDependencies (dependencies.go) and releases them on return.
func main() {
	ctx, stop := notifyInterrupt()
	err := rootCmd.ExecuteContext(ctx)
	interrupted := ctx.Err() != nil
	stop()
	if err != nil {
		// cobra has already reported the error on stderr.
		if interrupted {
			os.Exit(exitInterrupted)
		}
		os.Exit(1)
	}
}

// notifyInterrupt returns a context the first SIGINT or SIGTERM cancels, so a
// workflow unwinds through its own cleanup; a second signal exits at once.
func notifyInterrupt() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-signals:
		case <-ctx.Done():
			return
		}
		zerolog.Ctx(ctx).Warn().Msg("interrupted: cleaning up, interrupt again to exit at once")
		cancel()
		<-signals
		os.Exit(exitInterrupted)
	}()

	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}
