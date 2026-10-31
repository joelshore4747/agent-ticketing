// Package migrations embeds the SQL files beside it. It exists so the .sql files can stay at
// api/migrations/ where the docs say they are: go:embed cannot reach out of its own directory, so
// the alternative was moving them under internal/.
package migrations

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.sql
var FS embed.FS

// Names are the embedded migrations in the order Migrate applies them.
func Names() ([]string, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Latest is the newest embedded migration, which Migrate has applied by the time the listener
// opens. /api/version reports it so a caller can tell a running process from a checkout that has
// moved past it: a stale binary once created a batch a newer rule already forbade (TP-175).
func Latest() string {
	names, err := Names()
	if err != nil || len(names) == 0 {
		return "unknown"
	}
	return names[len(names)-1]
}
