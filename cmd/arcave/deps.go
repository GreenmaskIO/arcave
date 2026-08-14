package main

import (
	"os"

	arcaveclient "github.com/greenmaskio/arcave/internal/ng/client/arcave"
	"github.com/greenmaskio/arcave/internal/service/printer/json"
)

// newAPI returns the control plane client every handler sends its request with.
func newAPI() *arcaveclient.Stub {
	return arcaveclient.NewStub()
}

// newPrinter returns the printer results are rendered through.
func newPrinter() *json.Printer {
	return json.New(os.Stdout)
}
