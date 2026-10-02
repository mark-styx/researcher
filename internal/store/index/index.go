// Package index is the store's Postgres index (store.dsn). It holds runs,
// their captures and the sources they saw, all derived from the on-disk
// store, which stays the record of truth: a down database or a schema change
// delays indexing and loses nothing. See docs/research-store-design.md.
package index

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLock serializes migrations across processes (advisory lock key).
const migrateLock = 0x72677374 // "rgst"

// ErrNoDatabase means the server is up but the database in store.dsn
// doesn't exist yet. `researchguy store init` creates it.
var ErrNoDatabase = errors.New("index database does not exist; run `researchguy store init`")

// Index is an open connection pool to the index database.
type Index struct {
	pool       *pgxpool.Pool
	embedModel string
}

// Open connects to dsn and applies any pending migrations.
func Open(ctx context.Context, dsn string) (*Index, error) {
	ix, err := Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := ix.migrate(ctx); err != nil {
		ix.Close()
		return nil, err
	}
	return ix, nil
}

// Connect connects to dsn without migrating, for read-only checks such as
// `store doctor`.
func Connect(ctx context.Context, dsn string) (*Index, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("store.dsn is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing store.dsn: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to index: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		if isCode(err, "3D000") {
			return nil, ErrNoDatabase
		}
		return nil, fmt.Errorf("connecting to index %s: %w", Redact(dsn), err)
	}
	return &Index{pool: pool}, nil
}

// Close releases the pool.
func (ix *Index) Close() {
	if ix != nil {
		ix.pool.Close()
	}
}

// CreateDatabase creates the database named in dsn if it doesn't exist,
// connecting to the server's postgres database to do it. It reports whether
// it created one.
func CreateDatabase(ctx context.Context, dsn string) (bool, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false, fmt.Errorf("parsing store.dsn: %w", err)
	}
	name := cfg.Database
	if name == "" || name == "postgres" {
		return false, fmt.Errorf("store.dsn must name a database of its own, got %q", name)
	}
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return false, fmt.Errorf("connecting to %s: %w", Redact(dsn), err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking for database %s: %w", name, err)
	}
	if exists {
		return false, nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		if isCode(err, "42P04") { // created by someone else meanwhile
			return false, nil
		}
		return false, fmt.Errorf("creating database %s: %w", name, err)
	}
	return true, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, p := range paths {
		base := strings.TrimSuffix(strings.TrimPrefix(p, "migrations/"), ".sql")
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must be <number>_<name>.sql", p)
		}
		body, err := migrations.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: base, sql: string(body)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	return out, nil
}

// LatestVersion is the schema version this binary migrates to.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// migrate applies pending migrations in one transaction, under an advisory
// lock so concurrent processes don't race.
func (ix *Index) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("loading migrations: %w", err)
	}
	return pgx.BeginFunc(ctx, ix.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateLock)); err != nil {
			return fmt.Errorf("locking for migration: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return fmt.Errorf("creating schema_migrations: %w", err)
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
			return fmt.Errorf("reading schema version: %w", err)
		}
		if latest := ms[len(ms)-1].version; current > latest {
			return fmt.Errorf("index schema is version %d, newer than this researchguy (%d); upgrade researchguy", current, latest)
		}
		for _, m := range ms {
			if m.version <= current {
				continue
			}
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("applying migration %s: %w", m.name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
				return fmt.Errorf("recording migration %s: %w", m.name, err)
			}
		}
		return nil
	})
}

// SchemaVersion is the index's applied schema version, 0 before the first
// migration.
func (ix *Index) SchemaVersion(ctx context.Context) (int, error) {
	var exists bool
	if err := ix.pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return 0, err
	}
	var v int
	err := ix.pool.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func isCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

var dsnPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|\S+)`)

// Redact hides any password in a DSN, for messages.
func Redact(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "***")
			return u.String()
		}
		return dsn
	}
	return dsnPassword.ReplaceAllString(dsn, "${1}***")
}
