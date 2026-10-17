// Package apispec holds the Relic API's OpenAPI description.
package apispec

import _ "embed"

// JSON is the OpenAPI 3.1 description of the Relic API.
//
//go:embed relic.openapi.json
var JSON []byte
