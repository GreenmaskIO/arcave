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

// Command examples runs a photo album's workflows through a progress renderer:
//
//	go run ./pkg/progress/examples -scenario all -renderer auto -speed 1
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/rs/zerolog"
	"golang.org/x/term"

	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/renderer"
	"github.com/greenmaskio/arcave/pkg/progress/renderer/tty"
)

const (
	rendererAuto = "auto"
	rendererTTY  = "tty"
	rendererLog  = "log"
	rendererJSON = "json"
)

type scenario struct {
	name string
	run  func(p publisher, ctx context.Context, tracker *progress.Tracker) error
}

var scenarios = []scenario{
	{name: "publish", run: publisher.publish},
	{name: "backup", run: publisher.backup},
	{name: "parallel", run: publisher.parallel},
	{name: "unknown", run: publisher.unknown},
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		name     = flag.String("scenario", "all", "publish, backup, parallel, unknown or all")
		kind     = flag.String("renderer", rendererAuto, "auto, tty, log or json")
		speed    = flag.Float64("speed", 1, "how many times faster than real time the workflows run")
		selected []scenario
	)
	flag.Parse()
	for _, s := range scenarios {
		if *name == "all" || *name == s.name {
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 || *speed <= 0 || !slices.Contains([]string{rendererAuto, rendererTTY, rendererLog, rendererJSON}, *kind) {
		flag.Usage()
		return 2
	}
	if *kind == rendererAuto {
		*kind = rendererLog
		if term.IsTerminal(int(os.Stderr.Fd())) {
			*kind = rendererTTY
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		p    = publisher{clock: clock{speed: *speed}}
		code int
	)
	for _, s := range selected {
		if err := runScenario(ctx, *kind, p, s); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", s.name, err)
			code = 1
		}
		if ctx.Err() != nil {
			break
		}
	}
	return code
}

// runScenario runs s on its own tracker and renderer; it returns once the renderer has printed
// its final tree, so an error is printed after it.
func runScenario(ctx context.Context, kind string, p publisher, s scenario) error {
	noColor := os.Getenv("NO_COLOR") != ""
	switch kind {
	case rendererTTY:
		progressRenderer := tty.New(os.Stderr)
		logger := zerolog.New(zerolog.ConsoleWriter{
			Out:          progressRenderer.LogWriter(),
			NoColor:      noColor,
			PartsExclude: []string{zerolog.TimestampFieldName},
		}).Level(zerolog.WarnLevel)
		defer progressRenderer.Stop(tty.StopOptions{})
		return s.run(p, logger.WithContext(ctx), progress.New(progressRenderer, p.clock.Now))
	case rendererJSON:
		logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: noColor}).With().Timestamp().Logger()
		progressRenderer := renderer.NewJSON(os.Stdout)
		return s.run(p, logger.WithContext(ctx), progress.New(progressRenderer, p.clock.Now))
	default:
		logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: noColor}).With().Timestamp().Logger()
		return s.run(p, logger.WithContext(ctx), progress.New(renderer.NewLog(logger), p.clock.Now))
	}
}
