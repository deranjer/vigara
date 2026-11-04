// Package composemanagement embeds the portable manifest schemas used by writers.
package composemanagement

import "embed"

// Schemas are embedded so manifest validation does not depend on network access.
//
//go:embed server-profile.schema.json assignment.schema.json
var Schemas embed.FS
