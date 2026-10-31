// Package docs contains the offline manuals shipped with Wuko.
package docs

import "embed"

// Manuals is the canonical documentation used by the offline reference. Historical
// protocol documents remain available in the repository but are not indexed.
//
//go:embed *.md
var Manuals embed.FS
