package scheduler

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	runstore "github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/marklubin/researchguy/internal/store/index/indextest"
)

func finishedRun(t *testing.T, dir string) {
	t.Helper()
	st, err := runstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.StartRun(runstore.RunRecord{Kind: "dive", Topic: "t", Backend: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Append(runstore.Capture{Label: "web_fetch", Content: "page",
		Call: runstore.Call{Tool: "web_fetch", Action: "fetch", URL: "https://example.org/a"}}); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(runstore.Finish{Status: runstore.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
}

func TestRun_SyncsTheIndexWhenDSNIsSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)
	dsn := indextest.DSN(t)
	storeDir := filepath.Join(dir, "store")
	finishedRun(t, storeDir)

	logFile := filepath.Join(dir, "sched.log")
	cfg := &config.Config{
		Store: config.StoreConfig{Dir: storeDir, DSN: dsn},
		Scheduler: config.SchedulerConfig{
			PollInterval:  "50ms",
			MaxConcurrent: 1,
			LogFile:       logFile,
			PIDFile:       filepath.Join(dir, "sched.pid"),
		},
	}
	sched, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		sched.Run()
		close(done)
	}()
	defer func() {
		sched.Stop()
		<-done
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logFile)
		if strings.Contains(string(data), "Indexed 1 run(s)") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never indexed the run; log:\n%s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	ix, err := index.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if c, err := ix.Counts(context.Background()); err != nil || c.Runs != 1 || c.Sources != 1 {
		t.Fatalf("counts %+v, %v", c, err)
	}
}

func TestSyncIndex_LogsAProblemOnceAndTheRecovery(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	finishedRun(t, storeDir)
	var logs bytes.Buffer
	s := &Scheduler{
		cfg:    &config.Config{Store: config.StoreConfig{Dir: storeDir, DSN: "postgres://localhost:1/researchguy?connect_timeout=1"}},
		logger: log.New(&logs, "", 0),
	}
	s.syncIndex()
	s.syncIndex()
	if n := strings.Count(logs.String(), "Index sync error"); n != 1 {
		t.Fatalf("down index logged %d times, want once:\n%s", n, logs.String())
	}

	s.cfg.Store.DSN = indextest.DSN(t)
	s.syncIndex()
	s.syncIndex()
	defer s.index.Close()
	out := logs.String()
	if strings.Count(out, "Index sync recovered") != 1 || strings.Count(out, "Indexed 1 run(s)") != 1 {
		t.Fatalf("after recovery:\n%s", out)
	}
}

func TestSyncIndex_LogsRunsThatFailToIndexOnce(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	finishedRun(t, storeDir)
	st, err := runstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.RunIDs()
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if err := os.WriteFile(filepath.Join(st.RunDir(ids[0]), "run.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := &Scheduler{
		cfg:    &config.Config{Store: config.StoreConfig{Dir: storeDir, DSN: indextest.DSN(t)}},
		logger: log.New(&logs, "", 0),
	}
	defer func() { s.index.Close() }()
	s.syncIndex()
	s.syncIndex()
	if n := strings.Count(logs.String(), "1 run(s) failed to index; "+ids[0]); n != 1 {
		t.Fatalf("failed run logged %d times, want once:\n%s", n, logs.String())
	}
}
