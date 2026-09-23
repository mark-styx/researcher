package scheduler

import (
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/marklubin/researchguy/internal/config"
)

func TestShouldRun(t *testing.T) {
	// Create a minimal scheduler with a discard logger
	s := &Scheduler{logger: log.New(io.Discard, "", 0)}

	t.Run("nil cron returns false", func(t *testing.T) {
		task := &Task{ID: "aaaa1111-0000-0000-0000-000000000000"}
		if s.shouldRun(task) {
			t.Error("expected false for nil cron")
		}
	})

	t.Run("invalid cron returns false", func(t *testing.T) {
		bad := "not a cron"
		task := &Task{
			ID:   "aaaa2222-0000-0000-0000-000000000000",
			Cron: &bad,
		}
		if s.shouldRun(task) {
			t.Error("expected false for invalid cron")
		}
	})

	t.Run("past due returns true", func(t *testing.T) {
		every := "* * * * *" // every minute
		past := time.Now().Add(-2 * time.Hour)
		task := &Task{
			ID:        "aaaa3333-0000-0000-0000-000000000000",
			Cron:      &every,
			CreatedAt: past,
		}
		if !s.shouldRun(task) {
			t.Error("expected true for past-due task")
		}
	})

	t.Run("recently run returns false", func(t *testing.T) {
		hourly := "0 * * * *" // every hour
		now := time.Now()
		task := &Task{
			ID:        "aaaa4444-0000-0000-0000-000000000000",
			Cron:      &hourly,
			CreatedAt: now.Add(-24 * time.Hour),
			LastRunAt: &now, // just ran
		}
		if s.shouldRun(task) {
			t.Error("expected false for recently-run task")
		}
	})
}

func TestNewStore_FromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	cfg := &config.Config{
		Scheduler: config.SchedulerConfig{
			LogFile: filepath.Join(dir, "sched.log"),
			PIDFile: filepath.Join(dir, "sched.pid"),
		},
	}

	store, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	defer store.Close()

	// Verify we can perform operations on the store
	task := &Task{Type: "ask", Topic: "test", Status: StatusQueued}
	if err := store.Create(task); err != nil {
		t.Fatalf("Create on new store failed: %v", err)
	}
}

func TestNew_Success(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	cfg := &config.Config{
		Scheduler: config.SchedulerConfig{
			PollInterval:  "1s",
			MaxConcurrent: 1,
			LogFile:       filepath.Join(dir, "sched.log"),
			PIDFile:       filepath.Join(dir, "sched.pid"),
		},
	}

	// Use a nil-safe mock provider
	sched, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if sched == nil {
		t.Fatal("New() returned nil scheduler")
	}
	if sched.store == nil {
		t.Error("scheduler.store is nil")
	}
	if sched.logger == nil {
		t.Error("scheduler.logger is nil")
	}
	// Clean up
	sched.store.Close()
}

func TestStop_ClosesChannel(t *testing.T) {
	s := &Scheduler{
		logger: log.New(io.Discard, "", 0),
		stopCh: make(chan struct{}),
	}

	s.Stop()

	// Reading from a closed channel should return immediately
	select {
	case <-s.stopCh:
		// success — channel is closed
	default:
		t.Error("stopCh was not closed after Stop()")
	}
}

func TestStop_Idempotent(t *testing.T) {
	s := &Scheduler{
		logger: log.New(io.Discard, "", 0),
		stopCh: make(chan struct{}),
	}
	s.Stop()
	s.Stop() // should not panic
}

func TestRun_StopsOnSignal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	cfg := &config.Config{
		Scheduler: config.SchedulerConfig{
			PollInterval:  "100ms",
			MaxConcurrent: 1,
			LogFile:       filepath.Join(dir, "sched.log"),
			PIDFile:       filepath.Join(dir, "sched.pid"),
		},
	}

	sched, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		sched.Run()
		close(done)
	}()

	// Give it a moment to start, then stop
	time.Sleep(150 * time.Millisecond)
	sched.Stop()

	select {
	case <-done:
		// success — Run() returned
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop within 2s after Stop()")
	}
}

func TestNew_InvalidLogPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	cfg := &config.Config{
		Scheduler: config.SchedulerConfig{
			PollInterval:  "1s",
			MaxConcurrent: 1,
			LogFile:       "/nonexistent/dir/that/does/not/exist/sched.log",
			PIDFile:       filepath.Join(dir, "sched.pid"),
		},
	}

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("expected error for invalid log path")
	}
}

func TestPoll_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	store := newTestStore(t)
	s := &Scheduler{
		cfg: &config.Config{
			Scheduler: config.SchedulerConfig{
				PollInterval:  "1s",
				MaxConcurrent: 1,
			},
		},
		store:  store,
		logger: log.New(io.Discard, "", 0),
		stopCh: make(chan struct{}),
		sem:    make(chan struct{}, 1),
	}

	// poll should run without error on empty store
	s.poll()
}

func TestPoll_ScheduledNotDue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RESEARCHGUY_CONFIG_DIR", dir)

	store := newTestStore(t)

	// Create a scheduled task with a yearly cron and recent CreatedAt.
	// Next run will be Jan 1 next year, so shouldRun returns false.
	yearly := "0 0 1 1 *"
	task := &Task{
		Type:      "watch",
		Topic:     "test",
		Status:    StatusScheduled,
		Cron:      &yearly,
		CreatedAt: time.Now(),
	}
	store.Create(task)

	s := &Scheduler{
		cfg: &config.Config{
			Scheduler: config.SchedulerConfig{
				PollInterval:  "1s",
				MaxConcurrent: 1,
			},
		},
		store:  store,
		logger: log.New(io.Discard, "", 0),
		stopCh: make(chan struct{}),
		sem:    make(chan struct{}, 1),
	}

	// poll should check the scheduled task, find it not due, and check queued (empty)
	s.poll()
}

func TestStatusConstants(t *testing.T) {
	// Verify status constants are distinct non-empty strings
	statuses := []string{StatusQueued, StatusScheduled, StatusPlanned, StatusRunning, StatusDone, StatusFailed}
	seen := make(map[string]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("status constant is empty")
		}
		if seen[s] {
			t.Errorf("duplicate status constant: %q", s)
		}
		seen[s] = true
	}
}
