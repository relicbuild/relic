// Command routes writes the inventory of Relic's HTTP routes to
// compat/docs/routes.openapi.json.
package main

import (
	"fmt"
	"os"

	"relic/internal/app"
)

func main() {
	b, err := app.RouteInventory()
	if err == nil {
		//nolint:gosec // a documentation file
		err = os.WriteFile("api/adapters/artifactory/compat/docs/routes.openapi.json", b, 0o644)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}
