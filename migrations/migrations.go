// Package migrations embeds the SQL migration files so they ship inside the
// energystore-v2 binary. Used by internal/migrate.
package migrations

import "embed"

// FS is the embedded migrations directory. Files are read lexically; the
// 0001_init.sql / 0002_xxx.sql naming convention is load-bearing.
//
//go:embed *.sql
var FS embed.FS
