package model

// Printer renders a command's result. The production implementation writes
// indented JSON to stdout; tests capture the printed value instead.
type Printer interface {
	Print(v any) error
}
