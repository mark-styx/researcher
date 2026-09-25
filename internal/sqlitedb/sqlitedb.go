// Package sqlitedb opens researchguy's shared tasks.db with settings that let
// several researchguy processes use it at once (the scheduler daemon, the MCP
// server, and parallel `graph add-node` calls from orchestrated agents).
package sqlitedb

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// BusyTimeoutMS is how long a connection waits for a competing writer's lock
// before failing with SQLITE_BUSY.
const BusyTimeoutMS = 5000

// walRetries and walRetryDelay bound how long Open keeps retrying the switch
// to WAL journaling while other processes hold the database.
const (
	walRetries    = 50
	walRetryDelay = 100 * time.Millisecond
)

// DSN returns a modernc.org/sqlite DSN for path. The driver runs the _pragma
// on every new pooled connection, so a writer waits for a competing lock
// instead of failing immediately.
func DSN(path string) string {
	return fmt.Sprintf("%s?_pragma=busy_timeout(%d)", path, BusyTimeoutMS)
}

// Open opens the SQLite database at path using DSN and switches it to WAL
// journaling, which lets readers proceed during a write.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, err
	}
	if err := enableWAL(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// enableWAL sets journal_mode=WAL. The mode persists in the database file, so
// only the first switch does real work. SQLite doesn't wait on busy_timeout
// for that switch, so processes opening a rollback-journal database at the
// same moment can get SQLITE_BUSY; retry briefly instead of failing.
func enableWAL(db *sql.DB) error {
	var err error
	for attempt := 0; attempt < walRetries; attempt++ {
		var mode string
		if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err == nil {
			if mode != "wal" {
				return fmt.Errorf("enabling WAL: journal_mode is %q", mode)
			}
			return nil
		}
		if !isBusy(err) {
			return fmt.Errorf("enabling WAL: %w", err)
		}
		time.Sleep(walRetryDelay)
	}
	return fmt.Errorf("enabling WAL after %d attempts: %w", walRetries, err)
}

func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}
