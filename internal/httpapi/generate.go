// Package httpapi is the control plane's HTTP surface. openapi/ is the source:
// the .gen.go files come from `make generate` and are never edited by hand.
package httpapi

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=../../openapi/server.cfg.yaml ../../openapi/control-plane.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=../../openapi/types.cfg.yaml   ../../openapi/control-plane.yaml
