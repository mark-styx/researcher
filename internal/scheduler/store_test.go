package scheduler

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestStore creates a Store backed by a temp SQLite database.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrating test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer db.Close()

	// Run migrate twice — should not fail
	if err := migrate(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Verify table exists
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&name)
	if err != nil {
		t.Fatalf("tasks table not found: %v", err)
	}
}

func TestStore_CreateAndGet(t *testing.T) {
	s := newTestStore(t)

	task := &Task{
		Type:   "dive",
		Topic:  "quantum computing",
		Status: StatusQueued,
	}

	if err := s.Create(task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// ID should be auto-generated
	if task.ID == "" {
		t.Fatal("expected auto-generated ID")
	}
	if task.CreatedAt.IsZero() {
		t.Fatal("expected auto-set CreatedAt")
	}

	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Topic != "quantum computing" {
		t.Errorf("Topic = %q, want %q", got.Topic, "quantum computing")
	}
	if got.Status != StatusQueued {
		t.Errorf("Status = %q, want %q", got.Status, StatusQueued)
	}
}

func TestStore_GetByPrefix(t *testing.T) {
	s := newTestStore(t)

	task := &Task{
		ID:     "abc12345-6789-0000-0000-000000000000",
		Type:   "ask",
		Topic:  "test",
		Status: StatusQueued,
	}
	if err := s.Create(task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get("abc12345")
	if err != nil {
		t.Fatalf("Get by prefix: %v", err)
	}
	if got.ID != task.ID {
		t.Errorf("ID = %q, want %q", got.ID, task.ID)
	}
}

func TestStore_UpdateStatus(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	t.Run("running sets started_at", func(t *testing.T) {
		if err := s.UpdateStatus(task.ID, StatusRunning); err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}
		got, _ := s.Get(task.ID)
		if got.Status != StatusRunning {
			t.Errorf("Status = %q, want %q", got.Status, StatusRunning)
		}
		if got.StartedAt == nil {
			t.Error("StartedAt should be set")
		}
	})

	t.Run("done sets completed_at and last_run_at", func(t *testing.T) {
		if err := s.UpdateStatus(task.ID, StatusDone); err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}
		got, _ := s.Get(task.ID)
		if got.Status != StatusDone {
			t.Errorf("Status = %q, want %q", got.Status, StatusDone)
		}
		if got.CompletedAt == nil {
			t.Error("CompletedAt should be set")
		}
		if got.LastRunAt == nil {
			t.Error("LastRunAt should be set")
		}
	})
}

func TestStore_SetError(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.SetError(task.ID, "something broke"); err != nil {
		t.Fatalf("SetError: %v", err)
	}

	got, _ := s.Get(task.ID)
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error == nil || *got.Error != "something broke" {
		t.Errorf("Error = %v, want %q", got.Error, "something broke")
	}
}

func TestStore_SetOutputDir(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.SetOutputDir(task.ID, "/tmp/output"); err != nil {
		t.Fatalf("SetOutputDir: %v", err)
	}

	got, _ := s.Get(task.ID)
	if got.OutputDir == nil || *got.OutputDir != "/tmp/output" {
		t.Errorf("OutputDir = %v, want %q", got.OutputDir, "/tmp/output")
	}
}

func TestStore_SetMetadata(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.SetMetadata(task.ID, `{"mode":"hybrid"}`); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	got, _ := s.Get(task.ID)
	if got.Metadata == nil || *got.Metadata != `{"mode":"hybrid"}` {
		t.Errorf("Metadata = %v, want hybrid metadata JSON", got.Metadata)
	}
}

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.Delete(task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(task.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestStore_ListQueued(t *testing.T) {
	s := newTestStore(t)

	// Create tasks with different statuses and priorities
	s.Create(&Task{Type: "dive", Topic: "low", Status: StatusQueued, Priority: 1})
	s.Create(&Task{Type: "dive", Topic: "high", Status: StatusQueued, Priority: 10})
	s.Create(&Task{Type: "dive", Topic: "done", Status: StatusDone})

	tasks, err := s.ListQueued()
	if err != nil {
		t.Fatalf("ListQueued: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 queued tasks, got %d", len(tasks))
	}

	// Should be ordered by priority DESC
	if tasks[0].Topic != "high" {
		t.Errorf("first task topic = %q, want %q (highest priority)", tasks[0].Topic, "high")
	}
}

func TestStore_NextQueued(t *testing.T) {
	s := newTestStore(t)

	// Empty store
	got, err := s.NextQueued()
	if err != nil {
		t.Fatalf("NextQueued: %v", err)
	}
	if got != nil {
		t.Error("expected nil for empty store")
	}

	// Add tasks
	s.Create(&Task{Type: "dive", Topic: "low", Status: StatusQueued, Priority: 1})
	s.Create(&Task{Type: "dive", Topic: "high", Status: StatusQueued, Priority: 5})

	got, err = s.NextQueued()
	if err != nil {
		t.Fatalf("NextQueued: %v", err)
	}
	if got == nil {
		t.Fatal("expected a task")
	}
	if got.Topic != "high" {
		t.Errorf("Topic = %q, want %q", got.Topic, "high")
	}
}

func TestStore_ListScheduled(t *testing.T) {
	s := newTestStore(t)

	cron := "0 */6 * * *"
	s.Create(&Task{Type: "watch", Topic: "scheduled", Status: StatusScheduled, Cron: &cron})
	s.Create(&Task{Type: "dive", Topic: "no-cron", Status: StatusQueued})

	tasks, err := s.ListScheduled()
	if err != nil {
		t.Fatalf("ListScheduled: %v", err)
	}

	if len(tasks) != 1 {
		t.Fatalf("expected 1 scheduled task, got %d", len(tasks))
	}
	if tasks[0].Topic != "scheduled" {
		t.Errorf("Topic = %q, want %q", tasks[0].Topic, "scheduled")
	}
}

func TestStore_DueScheduled(t *testing.T) {
	s := newTestStore(t)

	cron := "0 */6 * * *"
	s.Create(&Task{Type: "watch", Topic: "due", Status: StatusScheduled, Cron: &cron})
	s.Create(&Task{Type: "watch", Topic: "queued", Status: StatusQueued, Cron: &cron})

	tasks, err := s.DueScheduled()
	if err != nil {
		t.Fatalf("DueScheduled: %v", err)
	}

	// Only "scheduled" status tasks returned
	if len(tasks) != 1 {
		t.Fatalf("expected 1 due task, got %d", len(tasks))
	}
	if tasks[0].Topic != "due" {
		t.Errorf("Topic = %q, want %q", tasks[0].Topic, "due")
	}
}

func TestStore_ClaimNextQueued(t *testing.T) {
	s := newTestStore(t)
	s.Create(&Task{Type: "dive", Topic: "high", Status: StatusQueued, Priority: 10})
	s.Create(&Task{Type: "dive", Topic: "low", Status: StatusQueued, Priority: 1})

	got, err := s.ClaimNextQueued("owner-a", 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNextQueued: %v", err)
	}
	if got == nil || got.Topic != "high" {
		t.Fatalf("claimed topic = %v, want high", got)
	}
	if got.LeaseOwner == nil || *got.LeaseOwner != "owner-a" {
		t.Fatalf("lease owner = %v, want owner-a", got.LeaseOwner)
	}

	// Claimed task should not be claimable again until released/expired.
	got2, err := s.ClaimNextQueued("owner-b", 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNextQueued second: %v", err)
	}
	if got2 == nil || got2.Topic != "low" {
		t.Fatalf("second claimed topic = %v, want low", got2)
	}
}

func TestStore_ClaimDueScheduledAndRelease(t *testing.T) {
	s := newTestStore(t)
	cron := "* * * * *"
	s.Create(&Task{Type: "watch", Topic: "scheduled-1", Status: StatusScheduled, Cron: &cron})
	s.Create(&Task{Type: "watch", Topic: "scheduled-2", Status: StatusScheduled, Cron: &cron})

	claimed, err := s.ClaimDueScheduled("owner-a", 30*time.Second, 1)
	if err != nil {
		t.Fatalf("ClaimDueScheduled: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed = %d, want 1", len(claimed))
	}
	if claimed[0].LeaseOwner == nil || *claimed[0].LeaseOwner != "owner-a" {
		t.Fatalf("lease owner = %v, want owner-a", claimed[0].LeaseOwner)
	}

	if err := s.ReleaseLease(claimed[0].ID, "owner-a"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	refetched, err := s.Get(claimed[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if refetched.LeaseOwner != nil || refetched.LeaseUntil != nil {
		t.Fatalf("expected lease cleared, got owner=%v until=%v", refetched.LeaseOwner, refetched.LeaseUntil)
	}
}

func TestStore_UpdateStatus_Failed(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "dive", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.UpdateStatus(task.ID, StatusFailed); err != nil {
		t.Fatalf("UpdateStatus(failed): %v", err)
	}
	got, _ := s.Get(task.ID)
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
}

func TestStore_UpdateStatus_Scheduled(t *testing.T) {
	s := newTestStore(t)

	task := &Task{Type: "watch", Topic: "test", Status: StatusQueued}
	s.Create(task)

	if err := s.UpdateStatus(task.ID, StatusScheduled); err != nil {
		t.Fatalf("UpdateStatus(scheduled): %v", err)
	}
	got, _ := s.Get(task.ID)
	if got.Status != StatusScheduled {
		t.Errorf("Status = %q, want %q", got.Status, StatusScheduled)
	}
}

func TestStore_CreateWithExplicitID(t *testing.T) {
	s := newTestStore(t)

	task := &Task{
		ID:     "explicit-id-123",
		Type:   "ask",
		Topic:  "test",
		Status: StatusQueued,
	}
	if err := s.Create(task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.ID != "explicit-id-123" {
		t.Errorf("ID = %q, want %q", task.ID, "explicit-id-123")
	}

	// Verify round-trip
	got, err := s.Get("explicit-id-123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "explicit-id-123" {
		t.Errorf("got ID = %q, want %q", got.ID, "explicit-id-123")
	}
}
