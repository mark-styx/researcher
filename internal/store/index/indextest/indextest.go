// Package indextest gives tests a throwaway Postgres database for the
// store index.
package indextest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Server is the Postgres server tests create databases on:
// RESEARCHGUY_TEST_PG, or the local default.
func Server() string {
	if dsn := os.Getenv("RESEARCHGUY_TEST_PG"); dsn != "" {
		return dsn
	}
	return "postgres://localhost:5432/postgres"
}

// DSN creates an empty database, drops it when the test ends, and returns
// its DSN. The test is skipped when no server is reachable.
func DSN(t testing.TB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, Server())
	if err != nil {
		t.Skipf("no Postgres for index tests (%v); set RESEARCHGUY_TEST_PG", err)
	}
	defer conn.Close(ctx)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "researchguy_test_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Drop(t, name) })
	return WithDatabase(t, Server(), name)
}

// Drop drops database name if it exists.
func Drop(t testing.TB, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, Server())
	if err != nil {
		t.Logf("dropping %s: %v", name, err)
		return
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		t.Logf("dropping %s: %v", name, err)
	}
}

// WithDatabase returns dsn pointing at database name instead.
func WithDatabase(t testing.TB, dsn, name string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndex(dsn, "/"+cfg.Database)
	if i < 0 {
		t.Fatalf("can't find database %q in %s", cfg.Database, dsn)
	}
	return dsn[:i] + "/" + name + dsn[i+1+len(cfg.Database):]
}
