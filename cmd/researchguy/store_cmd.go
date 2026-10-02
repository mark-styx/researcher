package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/spf13/cobra"
)

// exampleDSN is what the index setup messages suggest for store.dsn.
const exampleDSN = "postgres://localhost:5432/researchguy"

func storeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Manage the research store and its Postgres index",
		Long: `Every task writes a run record and the raw evidence it collected under
<store.dir>/runs/<run_id>/. That directory is the record of truth.

With store.dsn set, runs are also indexed in Postgres when they finish, and
the daemon catches up on any it missed. The index is derived from the store:
rebuild recreates it from scratch.`,
		GroupID: "project",
	}
	cmd.AddCommand(storeInitCmd(), storeIngestCmd(), storeRebuildCmd(), storeDoctorCmd())
	return cmd
}

func loadStore() (*config.Config, *store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	st, err := store.Open(cfg.Store.Dir)
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func requireDSN(cfg *config.Config) error {
	if strings.TrimSpace(cfg.Store.DSN) == "" {
		return fmt.Errorf("store.dsn is not set; add it to %s, for example dsn: %s",
			filepath.Join(config.Dir(), "config.yaml"), exampleDSN)
	}
	return nil
}

// openIndex opens the index in store.dsn, migrating it.
func openIndex(ctx context.Context, cfg *config.Config) (*index.Index, error) {
	if err := requireDSN(cfg); err != nil {
		return nil, err
	}
	return index.Open(ctx, cfg.Store.DSN)
}

func storeInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the index database in store.dsn and apply its schema",
		Long: `Init creates the database named in store.dsn if it doesn't exist, then
applies any pending schema migrations. Running it again is harmless.`,
		Example: `  researchguy store init`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Past arg parsing, an error is a finding, not a usage mistake.
			cmd.SilenceUsage = true
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := requireDSN(cfg); err != nil {
				return err
			}
			ctx := cmd.Context()
			dsn := cfg.Store.DSN
			created := false
			ix, err := index.Open(ctx, dsn)
			if errors.Is(err, index.ErrNoDatabase) {
				if created, err = index.CreateDatabase(ctx, dsn); err == nil {
					ix, err = index.Open(ctx, dsn)
				}
			}
			if err != nil {
				return err
			}
			defer ix.Close()
			v, err := ix.SchemaVersion(ctx)
			if err != nil {
				return fmt.Errorf("reading schema version: %w", err)
			}
			if created {
				fmt.Printf("Created index database: %s\n", index.Redact(dsn))
			} else {
				fmt.Printf("Index database: %s\n", index.Redact(dsn))
			}
			fmt.Printf("Schema version: %d\n", v)
			return nil
		},
	}
}

func storeIngestCmd() *cobra.Command {
	var runID string
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Index runs the index doesn't have yet",
		Long: `Ingest first marks interrupted any run whose process died before it
finished, then indexes every run that is new or has changed since it was
last indexed. Runs already up to date are skipped unless --force is set.
Ingest is idempotent, so replaying a run is safe.`,
		Example: `  researchguy store ingest
  researchguy store ingest --run 20261002T174145Z-7761e0
  researchguy store ingest --force --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, st, err := loadStore()
			if err != nil {
				return err
			}
			if runID != "" {
				if err := checkRunID(st, runID); err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			ix, err := openIndex(ctx, cfg)
			if err != nil {
				return err
			}
			defer ix.Close()

			var stats index.SyncStats
			if runID == "" && !force {
				stats, err = ix.Sync(ctx, st)
			} else {
				stats, err = ingestEach(ctx, ix, st, runID, force)
			}
			if err != nil {
				return err
			}
			if jsonOut {
				if err := printJSON(stats); err != nil {
					return err
				}
			} else {
				printSyncStats(stats, "Indexed")
			}
			return failedErr(stats)
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "index only this run")
	cmd.Flags().BoolVar(&force, "force", false, "re-index runs even when they're up to date")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// ingestEach reconciles, then ingests one run (runID) or all of them.
func ingestEach(ctx context.Context, ix *index.Index, st *store.Store, runID string, force bool) (index.SyncStats, error) {
	var stats index.SyncStats
	marked, err := st.Reconcile()
	stats.Interrupted = marked
	if err != nil {
		return stats, fmt.Errorf("reconciling runs: %w", err)
	}
	ids := []string{runID}
	if runID == "" {
		if ids, err = st.RunIDs(); err != nil {
			return stats, err
		}
	}
	for _, id := range ids {
		rs, err := ix.IngestRun(ctx, st.RunDir(id), force)
		switch {
		case err != nil:
			if stats.Failed == nil {
				stats.Failed = map[string]string{}
			}
			stats.Failed[id] = err.Error()
		case rs.Skipped:
			stats.UpToDate++
		default:
			stats.Ingested = append(stats.Ingested, rs)
		}
	}
	return stats, nil
}

// checkRunID rejects a --run that isn't a run directory in st.
func checkRunID(st *store.Store, id string) error {
	if id != filepath.Base(id) || id == "." || id == ".." {
		return fmt.Errorf("invalid run id %q", id)
	}
	if info, err := os.Stat(st.RunDir(id)); err != nil || !info.IsDir() {
		return fmt.Errorf("no run %s in %s", id, st.Dir())
	}
	return nil
}

func storeRebuildCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Empty the index and re-index every run in the store",
		Long: `Rebuild empties the index and indexes every run in the store again, in one
transaction: if it fails, the old index is left as it was. Use it after a
schema change or when the index looks wrong.`,
		Example: `  researchguy store rebuild`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, st, err := loadStore()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ix, err := openIndex(ctx, cfg)
			if err != nil {
				return err
			}
			defer ix.Close()
			stats, err := ix.Rebuild(ctx, st)
			if err != nil {
				return err
			}
			if jsonOut {
				if err := printJSON(stats); err != nil {
					return err
				}
			} else {
				printSyncStats(stats, "Rebuilt index from")
			}
			return failedErr(stats)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func printSyncStats(stats index.SyncStats, verb string) {
	if n := len(stats.Interrupted); n > 0 {
		fmt.Printf("Marked %d run(s) interrupted: %s\n", n, strings.Join(stats.Interrupted, ", "))
	}
	var captures, sources, sightings int
	for _, rs := range stats.Ingested {
		captures += rs.Captures
		sources += rs.Sources
		sightings += rs.Sightings
	}
	fmt.Printf("%s %d run(s): %d captures, %d sources, %d sightings", verb, len(stats.Ingested), captures, sources, sightings)
	if stats.UpToDate > 0 {
		fmt.Printf("; %d already up to date", stats.UpToDate)
	}
	fmt.Println()
	for _, id := range sortedKeys(stats.Failed) {
		fmt.Fprintf(os.Stderr, "Failed %s: %s\n", id, stats.Failed[id])
	}
}

func failedErr(stats index.SyncStats) error {
	if n := len(stats.Failed); n > 0 {
		return fmt.Errorf("%d run(s) failed to index", n)
	}
	return nil
}

// doctorReport is what `store doctor` found.
type doctorReport struct {
	StoreDir string       `json:"store_dir"`
	Store    store.Health `json:"store"`
	Index    *doctorIndex `json:"index,omitempty"` // nil when store.dsn is blank
	Problems []string     `json:"problems"`
}

type doctorIndex struct {
	DSN           string        `json:"dsn"` // password redacted
	Error         string        `json:"error,omitempty"`
	SchemaVersion int           `json:"schema_version"`
	LatestVersion int           `json:"latest_version"`
	Counts        *index.Counts `json:"counts,omitempty"`
	// Unindexed are finished runs the index lacks or has out of date.
	Unindexed []string `json:"unindexed,omitempty"`
	// Running are runs still in progress that aren't indexed yet; the
	// runner indexes them when they finish.
	Running []string `json:"running,omitempty"`
}

func storeDoctorCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the store and its index for problems",
		Long: `Doctor reads every run in the store and, when store.dsn is set, compares
it with the index. It changes nothing: it doesn't migrate the index or mark
dead runs interrupted (ingest does both). It exits non-zero when it finds a
problem.`,
		Example: `  researchguy store doctor
  researchguy store doctor --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cfg, st, err := loadStore()
			if err != nil {
				return err
			}
			rep, err := diagnose(cmd.Context(), cfg, st)
			if err != nil {
				return err
			}
			if jsonOut {
				if err := printJSON(rep); err != nil {
					return err
				}
			} else {
				printDoctor(rep)
			}
			if n := len(rep.Problems); n > 0 {
				return fmt.Errorf("%d problem(s) found", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return cmd
}

// doctorTimeout bounds how long doctor waits on the index.
const doctorTimeout = 30 * time.Second

func diagnose(ctx context.Context, cfg *config.Config, st *store.Store) (doctorReport, error) {
	h, err := st.Check()
	if err != nil {
		return doctorReport{}, fmt.Errorf("checking store: %w", err)
	}
	rep := doctorReport{StoreDir: st.Dir(), Store: h, Problems: []string{}}
	add := func(format string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(format, a...)) }
	if n := len(h.NoRecord); n > 0 {
		add("%d run dir(s) without a readable run.json: %s", n, strings.Join(h.NoRecord, ", "))
	}
	if n := len(h.Orphaned); n > 0 {
		add("%d run(s) left running by a process that exited (`store ingest` marks them interrupted): %s", n, strings.Join(h.Orphaned, ", "))
	}
	if n := len(h.CountMismatch); n > 0 {
		add("%d run(s) whose record's capture count doesn't match captures.jsonl: %s", n, strings.Join(h.CountMismatch, ", "))
	}
	for _, id := range sortedKeys(h.BadLines) {
		add("run %s has capture lines that don't decode: %v", id, h.BadLines[id])
	}
	if n := len(h.Truncated); n > 0 {
		add("%d finished run(s) whose capture log ends mid-line: %s", n, strings.Join(h.Truncated, ", "))
	}

	if strings.TrimSpace(cfg.Store.DSN) == "" {
		return rep, nil
	}
	di := &doctorIndex{DSN: index.Redact(cfg.Store.DSN), LatestVersion: index.LatestVersion()}
	rep.Index = di
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	ix, err := index.Connect(ctx, cfg.Store.DSN)
	if err != nil {
		di.Error = err.Error()
		add("index unreachable: %v", err)
		return rep, nil
	}
	defer ix.Close()
	if di.SchemaVersion, err = ix.SchemaVersion(ctx); err != nil {
		di.Error = err.Error()
		add("reading index schema version: %v", err)
		return rep, nil
	}
	switch {
	case di.SchemaVersion == 0:
		add("index has no schema yet (run `researchguy store init`)")
		return rep, nil
	case di.SchemaVersion > di.LatestVersion:
		add("index schema is version %d, newer than this researchguy (%d); upgrade researchguy", di.SchemaVersion, di.LatestVersion)
		return rep, nil
	case di.SchemaVersion < di.LatestVersion:
		add("index schema is version %d, this researchguy needs %d (`store ingest` or `store init` migrates it)", di.SchemaVersion, di.LatestVersion)
		return rep, nil
	}
	counts, err := ix.Counts(ctx)
	if err != nil {
		di.Error = err.Error()
		add("counting index rows: %v", err)
		return rep, nil
	}
	di.Counts = &counts
	pending, _, err := ix.Pending(ctx, st)
	if err != nil {
		di.Error = err.Error()
		add("comparing index with store: %v", err)
		return rep, nil
	}
	for _, id := range pending {
		if rec, err := store.ReadRecord(st.RunDir(id)); err == nil && rec.Status == store.StatusRunning {
			di.Running = append(di.Running, id)
			continue
		}
		di.Unindexed = append(di.Unindexed, id)
	}
	if n := len(di.Unindexed); n > 0 {
		add("%d finished run(s) not indexed or out of date (run `researchguy store ingest`)", n)
	}
	return rep, nil
}

func printDoctor(rep doctorReport) {
	h := rep.Store
	fmt.Printf("Store: %s\n", rep.StoreDir)
	fmt.Printf("  %d run(s), %d capture(s)", h.Runs, h.Captures)
	if h.Oldest != nil && h.Newest != nil {
		fmt.Printf(", %s to %s", h.Oldest.Format("2006-01-02"), h.Newest.Format("2006-01-02"))
	}
	fmt.Println()
	for _, status := range sortedKeys(h.ByStatus) {
		fmt.Printf("  %s: %d\n", status, h.ByStatus[status])
	}
	if n := len(h.Unverifiable); n > 0 {
		fmt.Printf("  %d running run(s) recorded no PID, so they can't be checked\n", n)
	}

	switch di := rep.Index; {
	case di == nil:
		fmt.Println("Index: off (store.dsn is not set)")
	case di.Error != "" && di.SchemaVersion == 0:
		fmt.Printf("Index: %s (unavailable)\n", di.DSN)
	default:
		fmt.Printf("Index: %s, schema %d of %d\n", di.DSN, di.SchemaVersion, di.LatestVersion)
		if c := di.Counts; c != nil {
			fmt.Printf("  %d run(s), %d capture(s), %d source(s), %d sighting(s)\n", c.Runs, c.Captures, c.Sources, c.Sightings)
		}
		if n := len(di.Running); n > 0 {
			fmt.Printf("  %d running run(s) not indexed yet\n", n)
		}
	}

	if len(rep.Problems) == 0 {
		fmt.Println("No problems found.")
		return
	}
	fmt.Println("Problems:")
	for _, p := range rep.Problems {
		fmt.Printf("  - %s\n", p)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
