package scheduler

import "time"

const (
	StatusQueued    = "queued"
	StatusScheduled = "scheduled"
	StatusPlanned   = "planned"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
)

type Task struct {
	ID           string
	Type         string
	Topic        string
	Status       string
	Cron         *string
	Backend      *string
	Model        *string
	Priority     int
	OutputDir    *string
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Error        *string
	LastRunAt    *time.Time
	Metadata     *string // JSON
	ParentTaskID *string
	ShardID      *string
	LeaseOwner   *string
	LeaseUntil   *time.Time
}
