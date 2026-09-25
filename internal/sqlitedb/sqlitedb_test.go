package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDSN_SetsBusyTimeout(t *testing.T) {
	dsn := DSN("/tmp/tasks.db")
	if dsn != "/tmp/tasks.db?_pragma=busy_timeout(5000)" {
		t.Errorf("DSN = %q", dsn)
	}
}

func TestOpen_AppliesBusyTimeoutAndWAL(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("reading journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("reading busy_timeout: %v", err)
	}
	if timeout != BusyTimeoutMS {
		t.Errorf("busy_timeout = %d, want %d", timeout, BusyTimeoutMS)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		db.Close()
	}
}

func TestOpen_MissingDirectoryFails(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing-dir", "tasks.db"))
	if err == nil {
		t.Fatal("expected Open to fail for a database in a nonexistent directory")
	}
	if !strings.Contains(err.Error(), "enabling WAL") {
		t.Errorf("error = %q, want it to mention enabling WAL", err)
	}
}

// A rollback-journal database held under an exclusive lock by another
// connection (standing in for another process) must not make Open fail; Open
// waits and switches to WAL once the lock is released.
func TestOpen_WaitsOutExclusiveLockOnRollbackJournalDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening holder: %v", err)
	}
	defer holder.Close()
	if _, err := holder.Exec("CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}

	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("BEGIN EXCLUSIVE: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		conn.ExecContext(ctx, "COMMIT")
		conn.Close()
		close(released)
	}()

	db, err := Open(path)
	<-released
	if err != nil {
		t.Fatalf("Open while locked: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("reading journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestIsBusy(t *testing.T) {
	if isBusy(nil) {
		t.Error("isBusy(nil) = true")
	}
	if isBusy(errors.New("database is locked")) {
		t.Error("isBusy should only match sqlite errors by code, not by message")
	}
}
