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

//go:build fidelity

package test

import (
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntrospectAssemble is the spec-fidelity catalog against the live daemon: minutes per
// engine, so it runs under the fidelity tag alone (make test-fidelity).
func TestIntrospectAssemble(t *testing.T) {
	for _, group := range fidelityGroups() {
		t.Run(group.name, func(t *testing.T) {
			for _, tc := range group.cases {
				t.Run(tc.name, func(t *testing.T) { runFidelityCase(t, tc) })
			}
		})
	}
}

// runFidelityCase brings the case's compose project up from its file, assembles a spec from
// the running containers, compares it with want, then brings it up again from that spec.
func runFidelityCase(t *testing.T, tc fidelityCase) {
	e := newFidelityEnv(t)
	if reason, ok := tc.unsupported[e.runtime]; ok {
		t.Skipf("%s refuses this compose project: %s", e.runtime, reason)
	}
	e.writeFiles(t, tc)
	if tc.setup != nil {
		tc.setup(t, e)
	}
	source := e.load(t, tc)
	e.build(t, source)
	e.up(t, tc, source)
	if tc.afterUp != nil {
		tc.afterUp(t, e)
	}

	r1 := e.introspect(t)
	specA, warns := e.assemble(t, r1)

	t.Run("pass1", func(t *testing.T) {
		defer func() {
			if t.Failed() {
				t.Logf("spec A:\n%s", e.marshal(t, specA))
			}
		}()
		// The project is named per run; a want names it only through the placeholder.
		want, err := e.codec.Parse(e.ctx, []byte(e.renderWant(tc)))
		require.NoError(t, err, "want must parse")
		delete(want.Extensions, "x-base")
		delete(want.Extensions, "x-engine")
		if want.Extensions[originExtension] == nil {
			assert.Equal(t, source.WorkingDir, specA.WorkingDir, "working_dir of the origin")
			assert.Equal(t, source.ComposeFiles, specA.ComposeFiles, "compose_files of the origin")
			want.WorkingDir, want.ComposeFiles = specA.WorkingDir, specA.ComposeFiles
			want.Extensions = specA.Extensions
		}

		diff := gocmp.Diff(want, specA, specOptions())

		e.expect(t, tc, "pass1", diff, "spec A against want (-want +A)")
		assert.Equal(t, normalizeWarnings(t, tc.warns), warns, "warnings")
	})

	t.Run("pass2", func(t *testing.T) {
		e.down(t, source)
		// compose writes into the project it creates, so it gets a copy of A.
		e.up(t, tc, e.reparse(t, specA))
		r2 := e.introspect(t)
		specB, _ := e.assemble(t, r2)
		if tc.drift != nil {
			tc.drift(&r1, e.runtime)
		}

		specDiff := gocmp.Diff(specA, specB, specOptions())
		resDiff := gocmp.Diff(r1, r2, introspectionOptions(declaredMACs(source), e.runtime), recreatedByRuntime(e.runtime))

		e.expect(t, tc, "pass2", specDiff+resDiff, "spec A against spec B, then R1 against R2 (-1 +2)")
	})
}
