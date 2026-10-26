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

package gc

import (
	"context"
	"errors"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// mockDel doubles the del DeleteOps runs on each op.
type mockDel struct{ mock.Mock }

func (m *mockDel) del(ctx context.Context, op model.GarbageOp) error {
	return m.Called(ctx, op.Target).Error(0)
}

func TestDeleteOps(t *testing.T) {
	ops := []model.GarbageOp{
		{Target: "a", Bytes: utils.New(int64(1))},
		{Target: "b", Bytes: utils.New(int64(2))},
		{Target: "c"},
	}
	deleted := func(n ...int) []model.GarbageDeleted {
		var out []model.GarbageDeleted
		for _, i := range n {
			out = append(out, model.GarbageDeleted{Target: ops[i].Target, Bytes: ops[i].Bytes})
		}
		return out
	}

	tests := []struct {
		name string
		// errs is what deleting each op returns, in order, one per op expected to start.
		errs []error
		// cancelAt cancels the run while deleting that op; cancelled cancels it before the first.
		cancelAt  int
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name:     "every op deleted, in order",
			errs:     []error{nil, nil, nil},
			cancelAt: -1,
			want:     model.GarbageDeletion{Deleted: deleted(0, 1, 2)},
		},
		{
			name:     "a target already gone counts as deleted, freeing nothing",
			errs:     []error{nil, &storages.MissingObjectsError{Paths: []string{"b"}}, nil},
			cancelAt: -1,
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: "a", Bytes: utils.New(int64(1))}, {Target: "b", Bytes: utils.New(int64(0))}, {Target: "c"},
			}},
		},
		{
			name:     "a failure is recorded and the rest still go",
			errs:     []error{errors.New("access denied"), nil, nil},
			cancelAt: -1,
			want: model.GarbageDeletion{
				Deleted: deleted(1, 2),
				Failed:  []model.GarbageDeleteFailure{{Target: "a", Error: "access denied"}},
			},
		},
		{
			name:      "a run cancelled before the first starts none",
			cancelAt:  -1,
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 3},
		},
		{
			name:     "a run cancelled midway starts no further deletion",
			errs:     []error{nil},
			cancelAt: 0,
			want:     model.GarbageDeletion{Deleted: deleted(0), NotRun: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			del := &mockDel{}
			var calls []*mock.Call
			for i, err := range tt.errs {
				call := del.On("del", mock.Anything, ops[i].Target).Return(err).Once()
				if i == tt.cancelAt {
					call.Run(func(mock.Arguments) { cancel() })
				}
				calls = append(calls, call)
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := DeleteOps(ctx, ops, del.del)

			// Assert
			assert.Equal(t, tt.want, got)
			del.AssertExpectations(t)
		})
	}
}
