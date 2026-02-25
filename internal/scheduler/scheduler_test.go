package scheduler

import (
	"io"
	"log"
	"testing"
	"time"
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

func TestStatusConstants(t *testing.T) {
	// Verify status constants are distinct non-empty strings
	statuses := []string{StatusQueued, StatusScheduled, StatusRunning, StatusDone, StatusFailed}
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
