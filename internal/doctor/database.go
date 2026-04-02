package doctor

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const catDatabase = "Database"

// requiredTables are the core tables that must exist for Tofi to function.
var requiredTables = []string{
	"users",
	"skills",
	"apps",
	"chat_sessions",
	"memories",
}

// CheckDatabase verifies SQLite database health.
func CheckDatabase(homeDir string) []CheckResult {
	var results []CheckResult
	dbPath := filepath.Join(homeDir, "tofi.db")

	// File exists?
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		results = append(results, newInfo(catDatabase, "Database", "tofi.db not found (normal on first run)"))
		return results
	}

	// Can open?
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		results = append(results, newFail(catDatabase, "Database", "cannot open: "+err.Error()))
		return results
	}
	defer db.Close()

	// Ping
	if err := db.Ping(); err != nil {
		results = append(results, newFail(catDatabase, "Database", "ping failed: "+err.Error()))
		return results
	}
	results = append(results, newOK(catDatabase, "Database", dbPath))

	// WAL mode?
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err == nil {
		if journalMode == "wal" {
			results = append(results, newOK(catDatabase, "Journal mode", "WAL"))
		} else {
			results = append(results, newWarn(catDatabase, "Journal mode", journalMode+" (expected WAL)"))
		}
	}

	// Required tables
	for _, table := range requiredTables {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			results = append(results, newWarn(catDatabase, "Table: "+table, "missing — restart daemon to create"))
		} else {
			results = append(results, newOK(catDatabase, "Table: "+table, ""))
		}
	}

	return results
}
