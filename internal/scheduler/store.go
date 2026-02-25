package scheduler

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/marklubin/researcher/internal/config"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func NewStore(cfg *config.Config) (*Store, error) {
	dbPath := filepath.Join(config.Dir(), "tasks.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id           TEXT PRIMARY KEY,
			type         TEXT NOT NULL,
			topic        TEXT NOT NULL,
			status       TEXT DEFAULT 'queued',
			cron         TEXT,
			backend      TEXT,
			model        TEXT,
			priority     INTEGER DEFAULT 0,
			output_dir   TEXT,
			created_at   DATETIME,
			started_at   DATETIME,
			completed_at DATETIME,
			error        TEXT,
			last_run_at  DATETIME,
			metadata     TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
		CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(priority DESC);
	`)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Create(t *Task) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}

	_, err := s.db.Exec(`
		INSERT INTO tasks (id, type, topic, status, cron, backend, model, priority, output_dir, created_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Type, t.Topic, t.Status, t.Cron, t.Backend, t.Model, t.Priority, t.OutputDir, t.CreatedAt, t.Metadata)
	return err
}

func (s *Store) Get(id string) (*Task, error) {
	row := s.db.QueryRow(`SELECT id, type, topic, status, cron, backend, model, priority, output_dir,
		created_at, started_at, completed_at, error, last_run_at, metadata
		FROM tasks WHERE id = ? OR id LIKE ?`, id, id+"%")
	return scanTask(row)
}

func (s *Store) UpdateStatus(id, status string) error {
	var err error
	switch status {
	case StatusRunning:
		now := time.Now()
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, started_at = ? WHERE id = ?`, status, now, id)
	case StatusDone:
		now := time.Now()
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, completed_at = ?, last_run_at = ? WHERE id = ?`, status, now, now, id)
	case StatusFailed:
		_, err = s.db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	default:
		_, err = s.db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	}
	return err
}

func (s *Store) SetError(id, errMsg string) error {
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, error = ? WHERE id = ?`, StatusFailed, errMsg, id)
	return err
}

func (s *Store) SetOutputDir(id, dir string) error {
	_, err := s.db.Exec(`UPDATE tasks SET output_dir = ? WHERE id = ?`, dir, id)
	return err
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? OR id LIKE ?`, id, id+"%")
	return err
}

func (s *Store) ListQueued() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata
		FROM tasks WHERE status = ? ORDER BY priority DESC, created_at ASC`, StatusQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ListScheduled() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata
		FROM tasks WHERE cron IS NOT NULL ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// NextQueued returns the highest-priority queued task.
func (s *Store) NextQueued() (*Task, error) {
	row := s.db.QueryRow(`
		SELECT id, type, topic, status, cron, backend, model, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata
		FROM tasks WHERE status = ? ORDER BY priority DESC, created_at ASC LIMIT 1`, StatusQueued)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// DueScheduled returns scheduled tasks whose cron expression indicates they should run.
func (s *Store) DueScheduled() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata
		FROM tasks WHERE status = ? AND cron IS NOT NULL`, StatusScheduled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanTask(s scanner) (*Task, error) {
	t := &Task{}
	err := s.Scan(&t.ID, &t.Type, &t.Topic, &t.Status, &t.Cron, &t.Backend, &t.Model,
		&t.Priority, &t.OutputDir, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
		&t.Error, &t.LastRunAt, &t.Metadata)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var tasks []*Task
	for rows.Next() {
		t := &Task{}
		err := rows.Scan(&t.ID, &t.Type, &t.Topic, &t.Status, &t.Cron, &t.Backend, &t.Model,
			&t.Priority, &t.OutputDir, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
			&t.Error, &t.LastRunAt, &t.Metadata)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
