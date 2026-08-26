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

package model

import (
	"fmt"
	"io"
)

// What a repository is asked, one type per operation. The address is judged on
// the way in, so a request that exists names an object this protocol has.

// ListBlobsRequest names the namespace to list.
type ListBlobsRequest struct {
	Type ObjectType
}

// NewListBlobsRequest judges a namespace segment.
func NewListBlobsRequest(t string) (ListBlobsRequest, error) {
	objectType, err := ParseObjectType(t)
	if err != nil {
		return ListBlobsRequest{}, fmt.Errorf("parse object type: %w", err)
	}
	return ListBlobsRequest{Type: objectType}, nil
}

// StatBlobRequest addresses the object whose size is asked for.
type StatBlobRequest struct {
	Type ObjectType
	ID   ObjectID
}

// NewStatBlobRequest judges both segments of an object's path.
func NewStatBlobRequest(t, name string) (StatBlobRequest, error) {
	objectType, id, err := parseAddress(t, name)
	if err != nil {
		return StatBlobRequest{}, fmt.Errorf("parse address: %w", err)
	}
	return StatBlobRequest{Type: objectType, ID: id}, nil
}

// Read is the read this stat resolved into: the address was judged once, on the
// way in, and the interval is the one the Range header resolved against the size
// this request answered with.
func (r StatBlobRequest) Read(offset, length int64) OpenBlobRangeRequest {
	return OpenBlobRangeRequest{
		Type:   r.Type,
		ID:     r.ID,
		Offset: offset,
		Length: length,
	}
}

// OpenBlobRangeRequest addresses an object and the interval of it to read. It
// has no constructor of its own: an interval is known only after a stat, so the
// only way to one is StatBlobRequest.Read.
type OpenBlobRangeRequest struct {
	Type   ObjectType
	ID     ObjectID
	Offset int64
	Length int64
}

// SaveBlobRequest addresses an object and carries the bytes to write under it.
type SaveBlobRequest struct {
	Type ObjectType
	ID   ObjectID
	Body io.Reader
}

// NewSaveBlobRequest judges both segments of an object's path and takes the body
// verbatim: what an upload carries is the client's, and this layer never reads it.
func NewSaveBlobRequest(t, name string, body io.Reader) (SaveBlobRequest, error) {
	objectType, id, err := parseAddress(t, name)
	if err != nil {
		return SaveBlobRequest{}, fmt.Errorf("parse address: %w", err)
	}
	return SaveBlobRequest{Type: objectType, ID: id, Body: body}, nil
}

// DeleteBlobRequest addresses the object to drop.
type DeleteBlobRequest struct {
	Type ObjectType
	ID   ObjectID
}

// NewDeleteBlobRequest judges both segments of an object's path.
func NewDeleteBlobRequest(t, name string) (DeleteBlobRequest, error) {
	objectType, id, err := parseAddress(t, name)
	if err != nil {
		return DeleteBlobRequest{}, fmt.Errorf("parse address: %w", err)
	}
	return DeleteBlobRequest{Type: objectType, ID: id}, nil
}

// parseAddress is the one place the two parsers are called together, so no
// request can be built carrying half an address. It stays unexported: what
// crosses the package boundary is a named request, never this pair.
func parseAddress(t, name string) (ObjectType, ObjectID, error) {
	objectType, err := ParseObjectType(t)
	if err != nil {
		return "", "", fmt.Errorf("parse object type: %w", err)
	}
	id, err := ParseObjectID(name)
	if err != nil {
		return "", "", fmt.Errorf("parse object id: %w", err)
	}
	return objectType, id, nil
}
