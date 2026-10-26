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

// Package eraser carries out the store's garbage actions, one GarbageEraser per action.
package eraser

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

// ObjectEraser deletes one object per op.
type ObjectEraser struct{}

func NewObjectEraser() ObjectEraser { return ObjectEraser{} }

func (s ObjectEraser) Action() model.GarbageAction { return model.GarbageActionDeleteObject }

// Erase deletes every object ops names, one at a time, since a batch Delete is refused whole for
// one object already gone and fails whole for one that cannot be removed.
func (s ObjectEraser) Erase(
	ctx context.Context, scope gc.ComposeProjectScope, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return scope.Store.Delete(ctx, op.Target)
	})
}

// TreeEraser deletes one directory per op, whole.
type TreeEraser struct{}

func NewTreeEraser() TreeEraser { return TreeEraser{} }

func (s TreeEraser) Action() model.GarbageAction { return model.GarbageActionDeleteTree }

// Erase deletes every directory ops names, whole.
func (s TreeEraser) Erase(
	ctx context.Context, scope gc.ComposeProjectScope, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return scope.Store.DeleteAll(ctx, op.Target)
	})
}

// ImageEraser deletes one stored image per op.
type ImageEraser struct{}

func NewImageEraser() ImageEraser { return ImageEraser{} }

func (s ImageEraser) Action() model.GarbageAction { return model.GarbageActionDeleteImage }

// Erase deletes every image directory ops names, each one's manifest first.
func (s ImageEraser) Erase(
	ctx context.Context, scope gc.ComposeProjectScope, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return imagecopy.DeleteStoredImage(ctx, scope.Store, op.Target)
	})
}
