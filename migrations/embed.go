// Package migrations embeds the SQLite schema migrations applied at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
