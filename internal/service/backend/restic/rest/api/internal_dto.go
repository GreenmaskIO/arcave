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

package api

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// The hand-written conversions between the specification's types and the
// domain's, here because this is the only package that sees both ends: repo must
// not know the specification, and the generated files must not be edited.

// ObjectTypeOf carries the namespace of a request into the domain. The generated
// binder converts the path segment but never judges it, so this is the door.
// It answers the one operation that asks the repository nothing: HeadBlobs only
// judges its segment, so building a request for a call that never happens would
// be a lie.
func ObjectTypeOf(t ObjectTypePath) (model.ObjectType, error) {
	return model.ParseObjectType(string(t))
}

// The five constructors an operation reaches its repository through. They are
// named for what they build — a repository's request, not another object of the
// specification — so a handler's one line says which side of the seam it is on.

// NewRepoListBlobsRequest carries a listing's namespace into the domain.
func NewRepoListBlobsRequest(r ListBlobsRequestObject) (model.ListBlobsRequest, error) {
	return model.NewListBlobsRequest(string(r.Type))
}

// NewRepoStatBlobRequest carries an object's address into the domain.
func NewRepoStatBlobRequest(r StatBlobRequestObject) (model.StatBlobRequest, error) {
	return model.NewStatBlobRequest(string(r.Type), string(r.ResticObjectID))
}

// NewRepoGetBlobRequest carries a read's address into the domain. A GET begins
// with a stat — the size decides the range and both answers render it — so what
// a GET's path carries is that stat's request; the interval follows it.
func NewRepoGetBlobRequest(r GetBlobRequestObject) (model.StatBlobRequest, error) {
	return model.NewStatBlobRequest(string(r.Type), string(r.ResticObjectID))
}

// NewRepoSaveBlobRequest carries an upload's address and its body into the domain.
func NewRepoSaveBlobRequest(r SaveBlobRequestObject) (model.SaveBlobRequest, error) {
	return model.NewSaveBlobRequest(string(r.Type), string(r.ResticObjectID), r.Body)
}

// NewRepoDeleteBlobRequest carries an object's address into the domain.
func NewRepoDeleteBlobRequest(r DeleteBlobRequestObject) (model.DeleteBlobRequest, error) {
	return model.NewDeleteBlobRequest(string(r.Type), string(r.ResticObjectID))
}

// Listing renders the listing — name and size per object, the only shape this
// server serves.
//
// The bytes are ours because oapi-codegen encodes only media types that are
// application/json or end in "+json", and restic's is neither; renaming it is
// not an option, since restic compares the header against that exact string.
func Listing(blobs []model.Blob) (ListBlobs200ApplicationvndXResticRestV2Response, error) {
	body, length, err := jsonBody(BlobListOf(blobs))
	if err != nil {
		return ListBlobs200ApplicationvndXResticRestV2Response{}, fmt.Errorf("json body: %w", err)
	}
	return ListBlobs200ApplicationvndXResticRestV2Response{Body: body, ContentLength: length}, nil
}

// BlobListOf renders a listing's objects. The slice is never nil, so an empty
// namespace marshals as [] and not null — a fresh repository is listed early.
func BlobListOf(blobs []model.Blob) BlobList {
	list := make(BlobList, 0, len(blobs))
	for _, b := range blobs {
		list = append(list, Blob{Name: b.Name, Size: b.Size})
	}
	return list
}

// jsonBody marshals before the response object is built, so a failure is still
// an error: an encoder writing into the response would have sent the status.
func jsonBody(payload any) (*bytes.Reader, int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("json marshal: %w", err)
	}
	return bytes.NewReader(data), int64(len(data)), nil
}
