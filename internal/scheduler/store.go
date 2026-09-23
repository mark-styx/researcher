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
			parent_task_id TEXT,
			shard_id     TEXT,
			priority     INTEGER DEFAULT 0,
			output_dir   TEXT,
			created_at   DATETIME,
			started_at   DATETIME,
			completed_at DATETIME,
			error        TEXT,
			last_run_at  DATETIME,
			metadata     TEXT,
			lease_owner  TEXT,
			lease_until  DATETIME
		);
		CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
		CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(priority DESC);
		CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
		CREATE INDEX IF NOT EXISTS idx_tasks_lease ON tasks(lease_until);
	`)
	if err != nil {
		return err
	}

	// Backfill columns for existing databases.
	if err := ensureColumn(db, "tasks", "parent_task_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(db, "tasks", "shard_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(db, "tasks", "lease_owner", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(db, "tasks", "lease_until", "DATETIME"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, colType string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if rows.Err() != nil {
		return rows.Err()
	}

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType))
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
		INSERT INTO tasks (id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir, created_at, metadata, lease_owner, lease_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Type, t.Topic, t.Status, t.Cron, t.Backend, t.Model, t.ParentTaskID, t.ShardID, t.Priority, t.OutputDir, t.CreatedAt, t.Metadata, t.LeaseOwner, t.LeaseUntil)
	return err
}

func (s *Store) Get(id string) (*Task, error) {
	row := s.db.QueryRow(`SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
		created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks WHERE id = ? OR id LIKE ?`, id, id+"%")
	return scanTask(row)
}

func (s *Store) UpdateStatus(id, status string) error {
	var err error
	switch status {
	case StatusRunning:
		now := time.Now()
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, started_at = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, status, now, id)
	case StatusDone:
		now := time.Now()
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, completed_at = ?, last_run_at = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, status, now, now, id)
	case StatusFailed:
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, status, id)
	default:
		_, err = s.db.Exec(`UPDATE tasks SET status = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, status, id)
	}
	return err
}

func (s *Store) SetError(id, errMsg string) error {
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, error = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, StatusFailed, errMsg, id)
	return err
}

func (s *Store) SetOutputDir(id, dir string) error {
	_, err := s.db.Exec(`UPDATE tasks SET output_dir = ? WHERE id = ?`, dir, id)
	return err
}

func (s *Store) SetMetadata(id, metadata string) error {
	_, err := s.db.Exec(`UPDATE tasks SET metadata = ? WHERE id = ?`, metadata, id)
	return err
}

func (s *Store) MarkScheduled(id string, lastRun time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, last_run_at = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?`, StatusScheduled, lastRun, id)
	return err
}

func (s *Store) ReleaseLease(id, owner string) error {
	_, err := s.db.Exec(`UPDATE tasks SET lease_owner = NULL, lease_until = NULL WHERE id = ? AND lease_owner = ?`, id, owner)
	return err
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? OR id LIKE ?`, id, id+"%")
	return err
}

func (s *Store) ListQueued() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks WHERE status = ? ORDER BY priority DESC, created_at ASC`, StatusQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ListScheduled() ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks WHERE cron IS NOT NULL ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// NextQueued returns the highest-priority queued task.
func (s *Store) NextQueued() (*Task, error) { // Deprecated: use ClaimNextQueued.
	return s.ClaimNextQueued("legacy", 30*time.Second)
}

// DueScheduled returns scheduled tasks whose cron expression indicates they should run.
func (s *Store) DueScheduled() ([]*Task, error) { // Deprecated: use ClaimDueScheduled.
	rows, err := s.db.Query(`
		SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks WHERE status = ? AND cron IS NOT NULL`, StatusScheduled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ClaimNextQueued(owner string, lease time.Duration) (*Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now()
	until := now.Add(lease)

	row := tx.QueryRow(`
		SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks
		WHERE status = ?
		  AND parent_task_id IS NULL
		  AND (lease_until IS NULL OR lease_until < ?)
		ORDER BY priority DESC, created_at ASC
		LIMIT 1`, StatusQueued, now)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}

	res, err := tx.Exec(`UPDATE tasks SET lease_owner = ?, lease_until = ? WHERE id = ? AND (lease_until IS NULL OR lease_until < ?)`,
		owner, until, t.ID, now)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t.LeaseOwner = &owner
	t.LeaseUntil = &until
	return t, nil
}

func (s *Store) ClaimDueScheduled(owner string, lease time.Duration, limit int) ([]*Task, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now()
	until := now.Add(lease)
	rows, err := tx.Query(`
		SELECT id, type, topic, status, cron, backend, model, parent_task_id, shard_id, priority, output_dir,
			created_at, started_at, completed_at, error, last_run_at, metadata, lease_owner, lease_until
		FROM tasks
		WHERE status = ?
		  AND cron IS NOT NULL
		  AND (lease_until IS NULL OR lease_until < ?)
		ORDER BY created_at ASC
		LIMIT ?`, StatusScheduled, now, limit)
	if err != nil {
		return nil, err
	}
	tasks, err := scanTasks(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}

	var claimed []*Task
	for _, t := range tasks {
		res, err := tx.Exec(`UPDATE tasks SET lease_owner = ?, lease_until = ? WHERE id = ? AND status = ? AND (lease_until IS NULL OR lease_until < ?)`,
			owner, until, t.ID, StatusScheduled, now)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		t.LeaseOwner = &owner
		t.LeaseUntil = &until
		claimed = append(claimed, t)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanTask(s scanner) (*Task, error) {
	t := &Task{}
	err := s.Scan(&t.ID, &t.Type, &t.Topic, &t.Status, &t.Cron, &t.Backend, &t.Model, &t.ParentTaskID, &t.ShardID,
		&t.Priority, &t.OutputDir, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
		&t.Error, &t.LastRunAt, &t.Metadata, &t.LeaseOwner, &t.LeaseUntil)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var tasks []*Task
	for rows.Next() {
		t := &Task{}
		err := rows.Scan(&t.ID, &t.Type, &t.Topic, &t.Status, &t.Cron, &t.Backend, &t.Model, &t.ParentTaskID, &t.ShardID,
			&t.Priority, &t.OutputDir, &t.CreatedAt, &t.StartedAt, &t.CompletedAt,
			&t.Error, &t.LastRunAt, &t.Metadata, &t.LeaseOwner, &t.LeaseUntil)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
