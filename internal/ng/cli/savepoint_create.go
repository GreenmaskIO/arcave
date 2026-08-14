package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/greenmaskio/arcave/internal/config"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// savepointCreateAPI is the control plane as this handler needs it.
type savepointCreateAPI interface {
	SavepointCreate(ctx context.Context, req apimodel.SavepointCreateRequest) (apimodel.SavepointCreateResponse, error)
}

// SavepointCreateParams is what the savepoint create command carries of its own.
type SavepointCreateParams struct {
	// Args is cobra's positional slice: the name may be given there instead of Name.
	Args        []string
	Name        string
	Description string
	// Labels are the repeated --label values, still as key=value text.
	Labels []string
}

type SavepointCreateHandler struct {
	api     savepointCreateAPI
	printer printer
}

func NewSavepointCreateHandler(api savepointCreateAPI, printer printer) *SavepointCreateHandler {
	return &SavepointCreateHandler{api: api, printer: printer}
}

// Create captures every stateful volume of the addressed stack.
func (h *SavepointCreateHandler) Create(
	ctx context.Context,
	cfg config.Config,
	a Addressing,
	params SavepointCreateParams,
) error {
	name, err := resolveSavepointName(params.Args, params.Name)
	if err != nil {
		return err
	}
	labels, err := parseLabels(params.Labels)
	if err != nil {
		return err
	}
	s, err := resolveScope(cfg, a)
	if err != nil {
		return err
	}

	req := apimodel.SavepointCreateRequest{
		ProjectName: s.ProjectName,
		SessionID:   s.SessionID,
		ProviderID:  s.ProviderID,
		StackID:     s.StackID,
		Name:        name,
		Description: params.Description,
		Labels:      labels,
	}
	if err := req.Validate(); err != nil {
		return err
	}
	res, err := h.api.SavepointCreate(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(res)
}

// resolveSavepointName reconciles the optional positional name with --name;
// giving both is an error only when they disagree.
func resolveSavepointName(args []string, flagValue string) (string, error) {
	if len(args) == 0 {
		return flagValue, nil
	}
	if flagValue != "" && flagValue != args[0] {
		return "", errors.New("savepoint name given both as argument and --name")
	}
	return args[0], nil
}

// parseLabels turns the repeated --label k=v into the map the request carries.
func parseLabels(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	labels := make(map[string]string, len(raw))
	for _, kv := range raw {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("label %q is not key=value", kv)
		}
		labels[key] = value
	}
	return labels, nil
}
