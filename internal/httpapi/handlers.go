package httpapi

import (
	"context"
	"net/http"
)

// statusOK is the only status the healthcheck reports.
const statusOK = "ok"

// Handlers implements the generated StrictServerInterface.
type Handlers struct {
	version string
}

var _ StrictServerInterface = (*Handlers)(nil)

// NewHandlers builds the API implementation. version is what `--version`
// reports.
func NewHandlers(version string) *Handlers { return &Handlers{version: version} }

// Healthcheck reports that the control plane is serving.
func (h *Handlers) Healthcheck(context.Context, HealthcheckRequestObject) (HealthcheckResponseObject, error) {
	return Healthcheck200JSONResponse{Status: statusOK, Version: h.version}, nil
}

// NewHTTPHandler wires the generated routing over the implementation.
func NewHTTPHandler(version string) http.Handler {
	return Handler(NewStrictHandler(NewHandlers(version), nil))
}
