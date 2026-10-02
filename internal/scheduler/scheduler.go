package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marklubin/researchguy/internal/config"
	"github.com/marklubin/researchguy/internal/embed"
	"github.com/marklubin/researchguy/internal/graph"
	"github.com/marklubin/researchguy/internal/llm"
	"github.com/marklubin/researchguy/internal/research"
	"github.com/marklubin/researchguy/internal/rollup"
	runstore "github.com/marklubin/researchguy/internal/store"
	"github.com/marklubin/researchguy/internal/store/index"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cfg        *config.Config
	store      *Store
	provider   llm.Provider
	logger     *log.Logger
	logFile    *os.File
	stopCh     chan struct{}
	stopOnce   sync.Once
	sem        chan struct{}
	wg         sync.WaitGroup
	owner      string
	graphStore *graph.Store   // nil unless graph.rollup.enabled
	rollup     *rollup.Rollup // nil unless graph.rollup.enabled
	index      *index.Index   // opened on the first index sync; nil after an error
	// indexStatus, fetchStatus, embedStatus, claimsStatus and linkStatus
	// are the last problem each store pass step logged, so a down index or
	// model is logged once rather than every pass.
	indexStatus, fetchStatus, embedStatus, claimsStatus, linkStatus string
	// passing is set while a store pass runs, so passes don't overlap.
	passing atomic.Bool
}

func New(cfg *config.Config, provider llm.Provider) (*Scheduler, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return nil, err
	}

	logFile := config.ExpandPath(cfg.Scheduler.LogFile)
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		store.Close()
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	maxConcurrent := cfg.Scheduler.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	logger := log.New(f, "scheduler: ", log.LstdFlags)

	s := &Scheduler{
		cfg:      cfg,
		store:    store,
		provider: provider,
		logger:   logger,
		logFile:  f,
		stopCh:   make(chan struct{}),
		sem:      make(chan struct{}, maxConcurrent),
		owner:    fmt.Sprintf("daemon-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}

	if cfg.Graph.Rollup.Enabled {
		gs, err := graph.NewStore(cfg)
		if err != nil {
			store.Close()
			f.Close()
			return nil, fmt.Errorf("opening graph store for rollup: %w", err)
		}
		s.graphStore = gs
		s.rollup = rollup.New(cfg, gs, provider, logger)
	}

	return s, nil
}

func (s *Scheduler) Run() {
	interval, err := time.ParseDuration(s.cfg.Scheduler.PollInterval)
	if err != nil {
		interval = 60 * time.Second
	}

	s.logger.Printf("Scheduler started, poll interval: %s", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// rollupCh stays nil (never selected) when rollup is disabled.
	var rollupCh <-chan time.Time
	if s.rollup != nil {
		rollupTicker := time.NewTicker(interval)
		defer rollupTicker.Stop()
		rollupCh = rollupTicker.C
		s.logger.Println("Graph rollup enabled")
	}

	// storeCh stays nil (never selected) when there's no index to sync and
	// no fetching to catch up on.
	var storeCh <-chan time.Time
	passCtx, cancelPass := context.WithCancel(context.Background())
	defer cancelPass()
	if s.cfg.Store.Dir != "" && (s.cfg.Store.DSN != "" || s.cfg.Store.Fetch.Enabled) {
		storeTicker := time.NewTicker(interval)
		defer storeTicker.Stop()
		storeCh = storeTicker.C
		if s.cfg.Store.DSN != "" {
			s.logger.Println("Store index sync enabled")
		}
		if s.cfg.Store.Fetch.Enabled {
			s.logger.Println("Source fetch catch-up enabled")
		}
		s.startStorePass(passCtx)
	}

	for {
		select {
		case <-ticker.C:
			s.poll()
		case <-storeCh:
			s.startStorePass(passCtx)
		case <-rollupCh:
			if err := s.rollup.Run(context.Background()); err != nil {
				s.logger.Printf("Rollup pass error: %v", err)
			}
		case <-s.stopCh:
			s.logger.Println("Scheduler stopped")
			cancelPass()
			s.wg.Wait()
			s.store.Close()
			if s.graphStore != nil {
				s.graphStore.Close()
			}
			s.index.Close()
			if s.logFile != nil {
				s.logFile.Close()
			}
			return
		}
	}
}

// indexSyncTimeout bounds one index catch-up pass.
const indexSyncTimeout = 5 * time.Minute

// syncIndex indexes the runs the index lacks: ones whose runner couldn't
// reach it, and runs a crash left behind (marked interrupted first). A
// problem is logged when it changes, not on every pass.
func (s *Scheduler) syncIndex(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, indexSyncTimeout)
	defer cancel()
	stats, err := s.indexPass(ctx)
	if len(stats.Interrupted) > 0 {
		s.logger.Printf("Marked %d run(s) interrupted: %s", len(stats.Interrupted), strings.Join(stats.Interrupted, ", "))
	}
	if len(stats.Ingested) > 0 {
		s.logger.Printf("Indexed %d run(s)", len(stats.Ingested))
	}
	status := ""
	switch {
	case err != nil:
		status = err.Error()
	case len(stats.Failed) > 0:
		ids := make([]string, 0, len(stats.Failed))
		for id := range stats.Failed {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		var b strings.Builder
		fmt.Fprintf(&b, "%d run(s) failed to index", len(ids))
		for _, id := range ids {
			fmt.Fprintf(&b, "; %s: %s", id, stats.Failed[id])
		}
		status = b.String()
	}
	s.logStatus(&s.indexStatus, "Index sync", status)
}

// logStatus logs a store pass step's problem when it differs from the last
// one logged, and its recovery.
func (s *Scheduler) logStatus(last *string, step, status string) {
	if status == *last {
		return
	}
	if status == "" {
		s.logger.Printf("%s recovered", step)
	} else {
		s.logger.Printf("%s error: %s", step, status)
	}
	*last = status
}

func (s *Scheduler) indexPass(ctx context.Context) (index.SyncStats, error) {
	if s.index == nil {
		ix, err := index.Open(ctx, s.cfg.Store.DSN)
		if err != nil {
			return index.SyncStats{}, err
		}
		ix.SetEmbedModel(embed.New(s.cfg).Model())
		s.index = ix
	}
	st, err := runstore.Open(s.cfg.Store.Dir)
	if err != nil {
		return index.SyncStats{}, err
	}
	stats, err := s.index.Sync(ctx, st)
	if err != nil {
		// Reopen next pass, which also migrates a schema changed meanwhile.
		s.index.Close()
		s.index = nil
	}
	return stats, err
}

func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *Scheduler) poll() {
	remaining := s.availableSlots()
	if remaining <= 0 {
		return
	}

	// Check scheduled (cron) tasks first
	scheduled, err := s.store.ClaimDueScheduled(s.owner, 30*time.Second, remaining)
	if err != nil {
		s.logger.Printf("Error listing scheduled tasks: %v", err)
	}

	for _, task := range scheduled {
		if remaining <= 0 {
			break
		}
		if s.shouldRun(task) {
			if s.dispatch(task) {
				remaining--
			}
		} else {
			_ = s.store.ReleaseLease(task.ID, s.owner)
		}
	}

	// Fill any remaining slots with queued one-shot tasks.
	for remaining > 0 {
		queued, err := s.store.ClaimNextQueued(s.owner, 30*time.Second)
		if err != nil {
			s.logger.Printf("Error getting next queued task: %v", err)
			return
		}

		if queued == nil {
			return
		}
		if !s.dispatch(queued) {
			return
		}
		remaining--
	}
}

func (s *Scheduler) availableSlots() int {
	return cap(s.sem) - len(s.sem)
}

func (s *Scheduler) shouldRun(task *Task) bool {
	if task.Cron == nil {
		return false
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(*task.Cron)
	if err != nil {
		s.logger.Printf("Invalid cron %q for task %s: %v", *task.Cron, task.ID[:8], err)
		return false
	}

	var lastRun time.Time
	if task.LastRunAt != nil {
		lastRun = *task.LastRunAt
	} else {
		lastRun = task.CreatedAt
	}

	nextRun := schedule.Next(lastRun)
	return time.Now().After(nextRun)
}

func (s *Scheduler) dispatch(task *Task) bool {
	select {
	case s.sem <- struct{}{}:
	default:
		return false
	}

	if err := s.store.UpdateStatus(task.ID, StatusRunning); err != nil {
		<-s.sem
		_ = s.store.ReleaseLease(task.ID, s.owner)
		s.logger.Printf("Error updating status: %v", err)
		return false
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }()
		s.execute(task)
	}()
	return true
}

func (s *Scheduler) execute(task *Task) {
	s.logger.Printf("Executing task %s: %s %q", task.ID[:8], task.Type, task.Topic)

	// Determine provider: use task overrides if set
	provider := s.provider
	if task.Backend != nil || task.Model != nil {
		backend := ""
		model := ""
		if task.Backend != nil {
			backend = *task.Backend
		}
		if task.Model != nil {
			model = *task.Model
		}
		p, err := llm.NewProvider(s.cfg, backend, model)
		if err != nil {
			s.logger.Printf("Error creating provider: %v", err)
			s.store.SetError(task.ID, err.Error())
			return
		}
		provider = p
	}

	runner := research.NewRunner(s.cfg, provider)
	result, err := runner.Run(context.Background(), research.Task{
		Type:    task.Type,
		Topic:   task.Topic,
		Sources: nil,
	})

	if err != nil {
		s.logger.Printf("Task %s failed: %v", task.ID[:8], err)
		s.store.SetError(task.ID, err.Error())
		// For recurring tasks, reset to scheduled so they run again next time
		if task.Cron != nil {
			s.store.UpdateStatus(task.ID, StatusScheduled)
		}
		return
	}

	if result.FilePath != "" {
		s.store.SetOutputDir(task.ID, result.FilePath)
	}
	if strings.TrimSpace(result.Metadata) != "" {
		if err := s.store.SetMetadata(task.ID, result.Metadata); err != nil {
			s.logger.Printf("Error updating metadata: %v", err)
		}
		if err := s.recordHybridChildren(task, result.Metadata); err != nil {
			s.logger.Printf("Error recording hybrid children: %v", err)
		}
	}

	// For recurring tasks, reset to scheduled; for one-shots, mark done
	if task.Cron != nil {
		now := time.Now()
		if err := s.store.MarkScheduled(task.ID, now); err != nil {
			s.logger.Printf("Error updating scheduled status: %v", err)
		}
		task.LastRunAt = &now
	} else {
		if err := s.store.UpdateStatus(task.ID, StatusDone); err != nil {
			s.logger.Printf("Error updating done status: %v", err)
		}
	}

	s.logger.Printf("Task %s completed: %s", task.ID[:8], result.FilePath)
}

type hybridWorkerRecord struct {
	Backend string `json:"backend"`
	Model   string `json:"model"`
	Shard   string `json:"shard"`
	Error   string `json:"error,omitempty"`
	Output  string `json:"output"`
}

type hybridMetadata struct {
	Mode    string               `json:"mode"`
	Workers []hybridWorkerRecord `json:"workers"`
}

func (s *Scheduler) recordHybridChildren(parent *Task, metadata string) error {
	var m hybridMetadata
	if err := json.Unmarshal([]byte(metadata), &m); err != nil {
		return nil
	}
	if m.Mode != "hybrid" || len(m.Workers) == 0 {
		return nil
	}

	for _, w := range m.Workers {
		parentID := parent.ID
		shard := w.Shard
		backend := w.Backend
		model := w.Model
		child := &Task{
			Type:         parent.Type,
			Topic:        parent.Topic,
			Status:       StatusPlanned,
			Backend:      &backend,
			Model:        &model,
			ParentTaskID: &parentID,
			ShardID:      &shard,
			CreatedAt:    time.Now(),
		}
		if err := s.store.Create(child); err != nil {
			continue
		}
		if strings.TrimSpace(w.Output) != "" {
			_ = s.store.SetMetadata(child.ID, w.Output)
		}
		if strings.TrimSpace(w.Error) != "" {
			_ = s.store.SetError(child.ID, w.Error)
		} else {
			_ = s.store.UpdateStatus(child.ID, StatusDone)
		}
	}
	return nil
}
