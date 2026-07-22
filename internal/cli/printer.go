package cli

// printer renders a handler's result. The concrete implementation is injected
// by cmd/arcave, so cli never imports internal/printer/json.
type printer interface {
	Print(v any) error
}
