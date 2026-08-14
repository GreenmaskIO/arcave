package cli

// printer renders a handler's result.
type printer interface {
	Print(v any) error
}
