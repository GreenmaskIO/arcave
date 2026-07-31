package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/api"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

type handler struct {
	repos model.RepositoryResolver
}

func New(repos model.RepositoryResolver) api.StrictServerInterface {
	return &handler{repos: repos}
}

// CreateRepository answers POST /{repoName}/?create=true: `restic init`'s first
// call, which reports that the repository exists rather than that this call made
// it.
func (h *handler) CreateRepository(ctx context.Context, request api.CreateRepositoryRequestObject) (api.CreateRepositoryResponseObject, error) {
	if request.Params.Create != "true" {
		return api.CreateRepository400Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.CreateRepository404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	// An existing repository is this call's success; the refusal belongs to SaveConfig.
	if err := repo.Create(ctx); err != nil && !errors.Is(err, model.ErrConfigExists) {
		return nil, fmt.Errorf("repo create: %w", err)
	}
	return api.CreateRepository200Response{}, nil
}

// DeleteRepository answers DELETE /{repoName}/: not implemented, because a
// repository's lifetime belongs to the layer that created the store it lives in.
// It resolves nothing: a 404 would pretend some other name would have worked.
func (h *handler) DeleteRepository(_ context.Context, _ api.DeleteRepositoryRequestObject) (api.DeleteRepositoryResponseObject, error) {
	return api.DeleteRepository501Response{}, nil
}

// StatConfig answers HEAD /{repoName}/config: how a client decides whether the
// repository is initialised at all, so the size matters as much as the status.
func (h *handler) StatConfig(ctx context.Context, request api.StatConfigRequestObject) (api.StatConfigResponseObject, error) {
	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.StatConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	size, err := repo.StatConfig(ctx)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.StatConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo stat config: %w", err)
	}
	return api.StatConfig200Response{
		Headers: api.StatConfig200ResponseHeaders{ContentLength: size},
	}, nil
}

// GetConfig answers GET /{repoName}/config: the one object of a repository that
// is addressed by no name and never listed.
func (h *handler) GetConfig(ctx context.Context, request api.GetConfigRequestObject) (api.GetConfigResponseObject, error) {
	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.GetConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	body, err := repo.OpenConfig(ctx)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.GetConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo open config: %w", err)
	}
	return api.GetConfig200ApplicationoctetStreamResponse{Body: body}, nil
}

// SaveConfig answers POST /{repoName}/config: `restic init`'s write, and the one
// call that refuses to overwrite what is already there.
func (h *handler) SaveConfig(ctx context.Context, request api.SaveConfigRequestObject) (api.SaveConfigResponseObject, error) {
	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.SaveConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	err = repo.SaveConfig(ctx, request.Body)
	switch {
	case errors.Is(err, model.ErrConfigExists):
		// The refusal restic's init depends on: never re-initialise a repository holding data.
		return api.SaveConfig403Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo save config: %w", err)
	}
	return api.SaveConfig200Response{}, nil
}

// DeleteConfig answers DELETE /{repoName}/config: the only way a repository is
// unmade, one object at a time, since DELETE /{repoName}/ is not implemented.
func (h *handler) DeleteConfig(ctx context.Context, request api.DeleteConfigRequestObject) (api.DeleteConfigResponseObject, error) {
	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.DeleteConfig404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	// Idempotent: a config already gone is the state this call asks for.
	if err := repo.DeleteConfig(ctx); err != nil && !errors.Is(err, model.ErrObjectNotFound) {
		return nil, fmt.Errorf("repo delete config: %w", err)
	}
	return api.DeleteConfig200Response{}, nil
}

// ListBlobs answers GET /{repoName}/{type}/: every object of one namespace, with
// its size, which is how restic learns what a repository holds.
func (h *handler) ListBlobs(ctx context.Context, request api.ListBlobsRequestObject) (api.ListBlobsResponseObject, error) {
	req, err := api.NewRepoListBlobsRequest(request)
	if err != nil {
		return api.ListBlobs404Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.ListBlobs404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	blobs, err := repo.ListBlobs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("repo list blobs: %w", err)
	}

	listing, err := api.Listing(blobs)
	if err != nil {
		return nil, fmt.Errorf("api listing: %w", err)
	}
	return listing, nil
}

// HeadBlobs answers HEAD /{repoName}/{type}/: present so that a HEAD of a
// listing is not a 405. The protocol does not describe it and restic never
// sends it.
func (h *handler) HeadBlobs(ctx context.Context, request api.HeadBlobsRequestObject) (api.HeadBlobsResponseObject, error) {
	if _, err := api.ObjectTypeOf(request.Type); err != nil {
		return api.HeadBlobs404Response{}, nil
	}

	// Resolved and dropped: the lookup is here only for its 404 — without it a
	// HEAD answers 200 for a repository that is not served.
	_, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.HeadBlobs404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}
	return api.HeadBlobs200Response{}, nil
}

// StatBlob answers HEAD /{repoName}/{type}/{name}: restic asks it to decide
// whether it already holds a pack.
func (h *handler) StatBlob(ctx context.Context, request api.StatBlobRequestObject) (api.StatBlobResponseObject, error) {
	req, err := api.NewRepoStatBlobRequest(request)
	if err != nil {
		return api.StatBlob404Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.StatBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	size, err := repo.StatBlob(ctx, req)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.StatBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo stat blob: %w", err)
	}
	return api.StatBlob200Response{
		Headers: api.StatBlob200ResponseHeaders{ContentLength: size},
	}, nil
}

// GetBlob answers GET /{repoName}/{type}/{name}: one object, whole or in part.
// The size is read first — the range is clamped against it and both answers
// render it.
func (h *handler) GetBlob(ctx context.Context, request api.GetBlobRequestObject) (api.GetBlobResponseObject, error) {
	stat, err := api.NewRepoGetBlobRequest(request)
	if err != nil {
		return api.GetBlob404Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.GetBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	size, err := repo.StatBlob(ctx, stat)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.GetBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo stat blob: %w", err)
	}

	interval, err := parseRange(utils.Value(request.Params.Range), size)
	if err != nil {
		return api.GetBlob416Response{
			Headers: api.GetBlob416ResponseHeaders{ContentRange: fmt.Sprintf("bytes */%d", size)},
		}, nil
	}
	if interval.requestedWholeBlob {
		return wholeBlob(ctx, repo, stat.Read(0, size), size)
	}
	return partialBlob(ctx, repo, stat.Read(interval.start, interval.length), size)
}

// wholeBlob answers the 200: the object entire. Accept-Ranges tells the client
// it may ask for a part of it next time.
func wholeBlob(ctx context.Context, repo model.Repository, req model.OpenBlobRangeRequest, size int64) (api.GetBlobResponseObject, error) {
	body, err := repo.OpenBlobRange(ctx, req)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.GetBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo open blob range: %w", err)
	}
	return api.GetBlob200ApplicationoctetStreamResponse{
		Body:          body,
		Headers:       api.GetBlob200ResponseHeaders{AcceptRanges: "bytes"},
		ContentLength: size,
	}, nil
}

// partialBlob answers the 206: the requested interval, and the size it was cut
// from, so the client knows what else it may ask for.
func partialBlob(ctx context.Context, repo model.Repository, req model.OpenBlobRangeRequest, size int64) (api.GetBlobResponseObject, error) {
	body, err := repo.OpenBlobRange(ctx, req)
	switch {
	case errors.Is(err, model.ErrObjectNotFound):
		return api.GetBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo open blob range: %w", err)
	}
	return api.GetBlob206ApplicationoctetStreamResponse{
		Body: body,
		Headers: api.GetBlob206ResponseHeaders{
			AcceptRanges: "bytes",
			ContentRange: fmt.Sprintf("bytes %d-%d/%d", req.Offset, req.Offset+req.Length-1, size),
		},
		ContentLength: req.Length,
	}, nil
}

// SaveBlob answers POST /{repoName}/{type}/{name}: an upload overwrites rather
// than refusing, since a name is content-addressed and carries the same bytes.
func (h *handler) SaveBlob(ctx context.Context, request api.SaveBlobRequestObject) (api.SaveBlobResponseObject, error) {
	req, err := api.NewRepoSaveBlobRequest(request)
	if err != nil {
		return api.SaveBlob404Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.SaveBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	if err := repo.SaveBlob(ctx, req); err != nil {
		return nil, fmt.Errorf("repo save blob: %w", err)
	}
	return api.SaveBlob200Response{}, nil
}

// DeleteBlob answers DELETE /{repoName}/{type}/{name}: one object of a prune,
// and the call restic retries.
func (h *handler) DeleteBlob(ctx context.Context, request api.DeleteBlobRequestObject) (api.DeleteBlobResponseObject, error) {
	req, err := api.NewRepoDeleteBlobRequest(request)
	if err != nil {
		// An address that could never name an object is not one already gone.
		return api.DeleteBlob404Response{}, nil
	}

	repo, err := h.repos.Lookup(request.RepoName)
	switch {
	case errors.Is(err, model.ErrRepositoryNotFound):
		logRepositoryNotFound(ctx, request.RepoName)
		return api.DeleteBlob404Response{}, nil
	case err != nil:
		return nil, fmt.Errorf("repo lookup: %w", err)
	}

	// Idempotent: a retry landing after the first attempt must not fail a prune.
	if err := repo.DeleteBlob(ctx, req); err != nil && !errors.Is(err, model.ErrObjectNotFound) {
		return nil, fmt.Errorf("repo delete blob: %w", err)
	}
	return api.DeleteBlob200Response{}, nil
}

// logRepositoryNotFound is worth an ERROR: no restic run arcave started asks for
// a repository arcave did not register.
func logRepositoryNotFound(ctx context.Context, name api.RepoNamePath) {
	zerolog.Ctx(ctx).Error().Str("repository", name).Msg("repository is not found")
}
