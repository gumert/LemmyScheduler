// Package migrations embeds the SQL schema migrations so the backend binary
// carries them and can apply them at startup, locally and when deployed.
//
// go:embed cannot reach parent directories, so the .sql files live in this
// directory next to this file. File names follow golang-migrate's
// {version}_{title}.{up|down}.sql convention.
package migrations

import "embed"

// FS holds every *.sql migration file at its root.
//
//go:embed *.sql
var FS embed.FS
