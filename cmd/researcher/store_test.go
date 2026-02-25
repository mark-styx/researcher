package main

import (
	"strings"
	"testing"
)

// --- queue ---

func TestQueueAdd(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "queue", "add", "--topic", "test queue topic", "--type", "dive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Queued task") {
		t.Errorf("output missing 'Queued task': %s", out)
	}
}

func TestQueueAdd_MissingTopic(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "queue", "add")
	if err == nil {
		t.Fatal("expected error for missing --topic")
	}
	if !strings.Contains(err.Error(), "--topic is required") {
		t.Errorf("error = %q, want to contain '--topic is required'", err.Error())
	}
}

func TestQueueList(t *testing.T) {
	testSetup(t)

	// Add a task first
	_, err := runCmd(t, "queue", "add", "--topic", "list test topic", "--type", "dive")
	if err != nil {
		t.Fatalf("queue add failed: %v", err)
	}

	out, err := runCmd(t, "queue", "list")
	if err != nil {
		t.Fatalf("queue list failed: %v", err)
	}

	if !strings.Contains(out, "list test topic") {
		t.Errorf("output missing topic: %s", out)
	}
}

// --- schedule ---

func TestScheduleAdd(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "schedule", "add", "--topic", "sched test", "--cron", "0 9 * * 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Scheduled task") {
		t.Errorf("output missing 'Scheduled task': %s", out)
	}
}

func TestScheduleAdd_MissingCron(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "schedule", "add", "--topic", "test")
	if err == nil {
		t.Fatal("expected error for missing --cron")
	}
	if !strings.Contains(err.Error(), "--cron is required") {
		t.Errorf("error = %q, want to contain '--cron is required'", err.Error())
	}
}

func TestScheduleAdd_WithBackendAndModel(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "schedule", "add", "--topic", "test", "--cron", "0 9 * * 1", "--backend", "ollama", "--model", "llama3", "--priority", "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Scheduled task") {
		t.Errorf("output missing 'Scheduled task': %s", out)
	}
}

func TestScheduleAdd_MissingTopic(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "schedule", "add", "--cron", "0 9 * * 1")
	if err == nil {
		t.Fatal("expected error for missing --topic")
	}
	if !strings.Contains(err.Error(), "--topic is required") {
		t.Errorf("error = %q, want to contain '--topic is required'", err.Error())
	}
}

func TestScheduleList(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "schedule", "add", "--topic", "sched list test", "--cron", "0 9 * * 1")
	if err != nil {
		t.Fatalf("schedule add failed: %v", err)
	}

	out, err := runCmd(t, "schedule", "list")
	if err != nil {
		t.Fatalf("schedule list failed: %v", err)
	}

	if !strings.Contains(out, "sched list test") {
		t.Errorf("output missing topic: %s", out)
	}
	if !strings.Contains(out, "0 9 * * 1") {
		t.Errorf("output missing cron expression: %s", out)
	}
}

func TestScheduleRemove(t *testing.T) {
	testSetup(t)

	// Add a task
	out, err := runCmd(t, "schedule", "add", "--topic", "to remove", "--cron", "0 0 * * *")
	if err != nil {
		t.Fatalf("schedule add failed: %v", err)
	}

	// Extract task ID from output (format: "Scheduled task <8chars>: ...")
	const prefix = "Scheduled task "
	idx := strings.Index(out, prefix)
	if idx < 0 {
		t.Fatalf("unexpected output format (no prefix %q): %s", prefix, out)
	}
	rest := out[idx+len(prefix):]
	// ID is everything up to the first ":"
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		t.Fatalf("unexpected output format (no colon): %s", out)
	}
	taskID := rest[:colonIdx]

	// Remove it
	out2, err := runCmd(t, "schedule", "remove", taskID)
	if err != nil {
		t.Fatalf("schedule remove failed: %v", err)
	}
	if !strings.Contains(out2, "Removed") {
		t.Errorf("output missing 'Removed': %s", out2)
	}
}

// --- watch ---

func TestWatchCmd(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "watch", "AI News")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Scheduled watch") {
		t.Errorf("output missing 'Scheduled watch': %s", out)
	}
}

func TestWatchCmd_CustomCron(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "watch", "test topic", "--cron", "0 */6 * * *")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "0 */6 * * *") {
		t.Errorf("output missing custom cron: %s", out)
	}
}

func TestWatchCmd_WithBackendAndModel(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "watch", "test topic", "--backend", "ollama", "--model", "llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Scheduled watch") {
		t.Errorf("output missing 'Scheduled watch': %s", out)
	}
}

func TestScheduleRemove_NonexistentID(t *testing.T) {
	testSetup(t)

	// SQLite DELETE with no matching rows doesn't error, so this should succeed
	out, err := runCmd(t, "schedule", "remove", "nonexistent-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("output missing 'Removed': %s", out)
	}
}

func TestQueueAdd_WithBackendAndModel(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "queue", "add", "--topic", "test", "--type", "dive", "--backend", "ollama", "--model", "llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Queued task") {
		t.Errorf("output missing 'Queued task': %s", out)
	}
}

func TestQueueAdd_WithPriority(t *testing.T) {
	testSetup(t)

	out, err := runCmd(t, "queue", "add", "--topic", "test", "--type", "ask", "--priority", "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Queued task") {
		t.Errorf("output missing 'Queued task': %s", out)
	}
}

func TestWatchCmd_MissingArg(t *testing.T) {
	testSetup(t)

	_, err := runCmd(t, "watch")
	if err == nil {
		t.Fatal("expected error for missing argument")
	}
}
