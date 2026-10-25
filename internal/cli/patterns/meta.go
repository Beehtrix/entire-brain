package patterns

import (
	"database/sql"
)

// CorpusMeta returns a corpus metadata value, or empty when it cannot be read.
func CorpusMeta(db *sql.DB, key string) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}
