// Package alt persists Address Lookup Table contents to prefetch.db --
// see cmd/alt.go, the only writer.
package alt

import _ "embed"

//go:embed schema.sql
var Schema string
