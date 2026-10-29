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
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
)

// exitInterrupted is the shell's code for a process ended by SIGINT.
const exitInterrupted = 130

// errInterruptedAgain ends the output of a run a second signal exits.
var errInterruptedAgain = errors.New("interrupted again: exited before cleaning up")

// main executes the command tree. Config resolution and logger setup happen in
// rootCmd's PersistentPreRunE (cmd_root.go); each command builds the dependencies
// it needs via newDependencies (dependencies.go) and releases them on return.
func main() {
	ctx, stop := notifyInterrupt()
	// A panic takes the live tree down first, or the terminal is left without its cursor.
	defer func() {
		if r := recover(); r != nil {
			closeOutput(runOutput, fmt.Errorf("panic: %v", r))
			panic(r)
		}
	}()
	err := rootCmd.ExecuteContext(ctx)
	interrupted := ctx.Err() != nil
	stop()
	// The output ends first, so the error comes after the final tree, or as the json stream's last line.
	closeOutput(runOutput, err)
	if err != nil {
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
		var out output
		if opened, ok := openedOutput.Load().(output); ok {
			out = opened
		}
		closeOutput(out, errInterruptedAgain)
		os.Exit(exitInterrupted)
	}()

	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}
