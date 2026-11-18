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

package utils

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockCloser is one thing a wiring opened.
type mockCloser struct{ mock.Mock }

func (m *mockCloser) Close() error { return m.Called().Error(0) }

// The stack unwinds newest first, and a closer that fails stops none of the older ones.
func TestClosers_Release(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store, engine, server := &mockCloser{}, &mockCloser{}, &mockCloser{}
	mock.InOrder(
		server.On("Close").Return(nil).Once(),
		engine.On("Close").Return(errors.New("boom")).Once(),
		store.On("Close").Return(nil).Once(),
	)
	var opened Closers
	opened.Add("store", store)
	opened.Add("engine", engine)
	opened.AddFunc(func(context.Context) { assert.NoError(t, server.Close()) })

	// Act
	opened.Release(ctx)

	// Assert
	store.AssertExpectations(t)
	engine.AssertExpectations(t)
	server.AssertExpectations(t)
}
