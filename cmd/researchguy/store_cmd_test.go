package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

// storeSetup is testSetup with store.dsn set to dsn. It returns the store
// the config points at.
func storeSetup(t *testing.T, dsn string) *store.Store {
	t.Helper()
	configDir, _ := testSetup(t)
	if dsn != "" {
		f, err := os.OpenFile(filepath.Join(configDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "store:\n  dsn: %q\n", dsn)
		f.Close()
	}
	st, err := store.Open(filepath.Join(configDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// finishedRun writes a finished run that searched for topic and saw urls.
func finishedRun(t *testing.T, st *store.Store, topic string, urls ...string) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: topic, Backend: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	call := store.Call{Tool: "web_search", Action: "search", Query: topic}
	for i, u := range urls {
		call.Results = append(call.Results, store.CaptureResult{Rank: i + 1, Title: "t", URL: u})
	}
	if _, err := run.Append(store.Capture{Backend: "ollama", Label: "web_search: " + topic, Content: "results", Call: call}); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(store.Finish{Status: store.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	return run
}

// orphanedRun writes a run left running by a process that has exited.
func orphanedRun(t *testing.T, st *store.Store) *store.Run {
	t.Helper()
	run, err := st.StartRun(store.RunRecord{Kind: "dive", Topic: "crashed", Backend: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	editRecord(t, run.Dir(), func(rec *store.RunRecord) { rec.PID = cmd.Process.Pid })
	return run
}

func editRecord(t *testing.T, dir string, edit func(*store.RunRecord)) {
	t.Helper()
	rec, err := store.ReadRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(&rec)
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func decode[T any](t *testing.T, stdout string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	return v
}

func TestStoreCmd_WithoutDSN(t *testing.T) {
	storeSetup(t, "")
	for _, args := range [][]string{{"store", "init"}, {"store", "ingest"}, {"store", "rebuild"}} {
		_, stderr, err := runCmdStdout(t, args...)
		if err == nil || !strings.Contains(err.Error(), "store.dsn is not set") {
			t.Fatalf("%v: want a store.dsn error, got %v\n%s", args, err, stderr)
		}
	}
	stdout, _, err := runCmdStdout(t, "store", "doctor")
	if err != nil {
		t.Fatalf("doctor on an empty store with no index: %v", err)
	}
	if !strings.Contains(stdout, "Index: off") || !strings.Contains(stdout, "No problems found.") {
		t.Fatalf("got %q", stdout)
	}
}

func TestStoreInit_CreatesTheDatabaseOnce(t *testing.T) {
	// Probe for a server; the database this test creates is a fresh name.
	indextest.DSN(t)
	name := fmt.Sprintf("researchguy_test_init_%d", os.Getpid())
	t.Cleanup(func() { indextest.Drop(t, name) })
	storeSetup(t, indextest.WithDatabase(t, indextest.Server(), name))

	stdout, stderr, err := runCmdStdout(t, "store", "init")
	if err != nil {
		t.Fatalf("store init: %v\n%s", err, stderr)
	}
	want := fmt.Sprintf("Schema version: %d", index.LatestVersion())
	if !strings.Contains(stdout, "Created index database") || !strings.Contains(stdout, want) {
		t.Fatalf("first init: %q", stdout)
	}
	stdout, _, err = runCmdStdout(t, "store", "init")
	if err != nil || strings.Contains(stdout, "Created") || !strings.Contains(stdout, want) {
		t.Fatalf("second init: %v %q", err, stdout)
	}
}

func TestStoreCmd_IngestDoctorRebuild(t *testing.T) {
	st := storeSetup(t, indextest.DSN(t))
	a := finishedRun(t, st, "go generics", "https://go.dev/blog/go1.18", "https://example.org/a")
	finishedRun(t, st, "rust traits", "https://example.org/a")
	orphan := orphanedRun(t, st)

	// A fresh database has no schema; doctor reports it and changes nothing.
	stdout, _, err := runCmdStdout(t, "store", "doctor", "--json")
	if err == nil {
		t.Fatal("doctor should fail before the index has a schema")
	}
	rep := decode[doctorReport](t, stdout)
	if rep.Index == nil || rep.Index.SchemaVersion != 0 || !strings.Contains(strings.Join(rep.Problems, "\n"), "no schema yet") {
		t.Fatalf("doctor before init: %+v", rep)
	}
	if len(rep.Store.Orphaned) != 1 || rep.Store.Orphaned[0] != orphan.ID() {
		t.Fatalf("doctor should list the orphaned run: %+v", rep.Store)
	}

	stdout, stderr, err := runCmdStdout(t, "store", "ingest", "--json")
	if err != nil {
		t.Fatalf("ingest: %v\n%s", err, stderr)
	}
	stats := decode[index.SyncStats](t, stdout)
	if len(stats.Interrupted) != 1 || stats.Interrupted[0] != orphan.ID() || len(stats.Ingested) != 3 || stats.UpToDate != 0 {
		t.Fatalf("first ingest: %+v", stats)
	}

	stdout, stderr, err = runCmdStdout(t, "store", "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor after ingest: %v\n%s\n%s", err, stdout, stderr)
	}
	rep = decode[doctorReport](t, stdout)
	if c := rep.Index.Counts; c == nil || c.Runs != 3 || c.Sources != 2 || len(rep.Problems) != 0 || rep.Store.ByStatus[store.StatusInterrupted] != 1 {
		t.Fatalf("doctor after ingest: %+v %+v", rep, rep.Index)
	}

	stdout, _, err = runCmdStdout(t, "store", "ingest")
	if err != nil || !strings.Contains(stdout, "Indexed 0 run(s)") || !strings.Contains(stdout, "3 already up to date") {
		t.Fatalf("second ingest: %v %q", err, stdout)
	}

	stdout, _, err = runCmdStdout(t, "store", "ingest", "--run", a.ID(), "--force", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if stats := decode[index.SyncStats](t, stdout); len(stats.Ingested) != 1 || stats.Ingested[0].RunID != a.ID() {
		t.Fatalf("forced ingest of one run: %+v", stats)
	}

	// A run changed after it was indexed shows as unindexed until ingest.
	editRecord(t, a.Dir(), func(rec *store.RunRecord) { rec.ReportPath = "/moved/report.md" })
	stdout, _, err = runCmdStdout(t, "store", "doctor")
	if err == nil || !strings.Contains(stdout, "1 finished run(s) not indexed or out of date") {
		t.Fatalf("doctor after a change: %v %q", err, stdout)
	}

	stdout, stderr, err = runCmdStdout(t, "store", "rebuild", "--json")
	if err != nil {
		t.Fatalf("rebuild: %v\n%s", err, stderr)
	}
	if stats := decode[index.SyncStats](t, stdout); len(stats.Ingested) != 3 {
		t.Fatalf("rebuild: %+v", stats)
	}
	stdout, _, err = runCmdStdout(t, "store", "doctor")
	if err != nil || !strings.Contains(stdout, "No problems found.") {
		t.Fatalf("doctor after rebuild: %v %q", err, stdout)
	}
}

func TestStoreIngest_RejectsBadRunIDs(t *testing.T) {
	storeSetup(t, "postgres://localhost:1/researchguy?connect_timeout=1")
	for id, want := range map[string]string{"../x": "invalid run id", "nope": "no run nope"} {
		_, _, err := runCmdStdout(t, "store", "ingest", "--run", id)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--run %s: want %q, got %v", id, want, err)
		}
	}
}

func TestStoreDoctor_ReportsStoreAndIndexProblems(t *testing.T) {
	st := storeSetup(t, "postgres://mark:secret@localhost:1/researchguy?connect_timeout=1")
	finishedRun(t, st, "ok", "https://example.org/a")
	if err := os.MkdirAll(st.RunDir("20260101T000000Z-000000"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCmdStdout(t, "store", "doctor", "--json")
	if err == nil {
		t.Fatal("doctor should fail with problems")
	}
	if strings.Contains(stdout+stderr, "secret") {
		t.Fatalf("doctor printed the password:\n%s\n%s", stdout, stderr)
	}
	rep := decode[doctorReport](t, stdout)
	problems := strings.Join(rep.Problems, "\n")
	if rep.Store.Runs != 1 || !strings.Contains(problems, "without a readable run.json") || !strings.Contains(problems, "index unreachable") || rep.Index.Error == "" {
		t.Fatalf("got %+v", rep)
	}
}

func TestStoreIngest_ReportsRunsThatFailToIndex(t *testing.T) {
	st := storeSetup(t, indextest.DSN(t))
	finishedRun(t, st, "ok", "https://example.org/a")
	bad := finishedRun(t, st, "bad")
	if err := os.WriteFile(filepath.Join(bad.Dir(), "run.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --force without --run re-indexes every run, one at a time.
	stdout, stderr, err := runCmdStdout(t, "store", "ingest", "--force")
	if err == nil || !strings.Contains(err.Error(), "1 run(s) failed to index") {
		t.Fatalf("want a failure exit, got %v", err)
	}
	if !strings.Contains(stdout, "Indexed 1 run(s)") || !strings.Contains(stderr, "Failed "+bad.ID()) {
		t.Fatalf("stdout %q\nstderr %q", stdout, stderr)
	}

	stdout, _, err = runCmdStdout(t, "store", "rebuild", "--json")
	if err == nil {
		t.Fatal("rebuild should exit non-zero when a run can't be read")
	}
	if stats := decode[index.SyncStats](t, stdout); len(stats.Ingested) != 1 || stats.Failed[bad.ID()] == "" {
		t.Fatalf("rebuild: %+v", stats)
	}
}

func TestStoreDoctor_IndexSchemaNewerThanBinary(t *testing.T) {
	dsn := indextest.DSN(t)
	storeSetup(t, dsn)
	if _, _, err := runCmdStdout(t, "store", "init"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES (9999, 'future')`)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmdStdout(t, "store", "doctor")
	if err == nil || !strings.Contains(stdout, "newer than this researchguy") {
		t.Fatalf("got %v %q", err, stdout)
	}
}
