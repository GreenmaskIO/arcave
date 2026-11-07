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

// Package eraser carries out the gc's actions, one GarbageEraser per action.
package eraser

import (
	"context"

	"github.com/greenmaskio/storages"

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
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return store.Delete(ctx, op.Target)
	})
}

// TreeEraser deletes one directory per op, whole.
type TreeEraser struct{}

func NewTreeEraser() TreeEraser { return TreeEraser{} }

func (s TreeEraser) Action() model.GarbageAction { return model.GarbageActionDeleteTree }

// Erase deletes every directory ops names, whole.
func (s TreeEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return store.DeleteAll(ctx, op.Target)
	})
}

// SavepointDeleter deletes a savepoint, its record and then its tree.
type SavepointDeleter interface {
	DeleteSavepoint(ctx context.Context, id model.SavepointID) error
}

// SavepointEraser deletes one committed savepoint per op.
type SavepointEraser struct {
	newDeleter func(store storages.Storager) SavepointDeleter
}

// NewSavepointEraser deletes through what newDeleter builds over a compose project's store.
func NewSavepointEraser(newDeleter func(store storages.Storager) SavepointDeleter) SavepointEraser {
	return SavepointEraser{newDeleter: newDeleter}
}

func (s SavepointEraser) Action() model.GarbageAction { return model.GarbageActionDeleteSavepoint }

// Erase deletes every savepoint ops names; one that fails past its record leaves a dead tree.
// Blobs and restic snapshots go on the next run: this run's scope still read them as live.
func (s SavepointEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	deleter := s.newDeleter(store)
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return deleter.DeleteSavepoint(ctx, model.SavepointID(op.Target))
	})
}

// ImageEraser deletes one stored image per op.
type ImageEraser struct{}

func NewImageEraser() ImageEraser { return ImageEraser{} }

func (s ImageEraser) Action() model.GarbageAction { return model.GarbageActionDeleteImage }

// Erase deletes every image directory ops names, each one's manifest first.
func (s ImageEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return imagecopy.DeleteStoredImage(ctx, store, op.Target)
	})
}

// tagRemover is the daemon as TagEraser takes a reference off it.
type tagRemover interface {
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// TagEraser takes restored layer tags off the daemon, one per op.
type TagEraser struct {
	daemon tagRemover
}

func NewTagEraser(daemon tagRemover) TagEraser {
	return TagEraser{daemon: daemon}
}

func (s TagEraser) Action() model.GarbageAction { return model.GarbageActionUntag }

// Erase takes every tag ops name off the daemon: a tag whose image a container was created from
// since it was found fails alone.
func (s TagEraser) Erase(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return s.daemon.RemoveImageRef(ctx, model.ImageRef(op.Target))
	})
}
