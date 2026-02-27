package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/marklubin/researcher/internal/config"
	"github.com/marklubin/researcher/internal/llm"
	"github.com/marklubin/researcher/internal/research"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cfg      *config.Config
	store    *Store
	provider llm.Provider
	logger   *log.Logger
	stopCh   chan struct{}
}

func New(cfg *config.Config, provider llm.Provider) (*Scheduler, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return nil, err
	}

	logFile := config.ExpandPath(cfg.Scheduler.LogFile)
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	return &Scheduler{
		cfg:      cfg,
		store:    store,
		provider: provider,
		logger:   log.New(f, "scheduler: ", log.LstdFlags),
		stopCh:   make(chan struct{}),
	}, nil
}

func (s *Scheduler) Run() {
	interval, err := time.ParseDuration(s.cfg.Scheduler.PollInterval)
	if err != nil {
		interval = 60 * time.Second
	}

	s.logger.Printf("Scheduler started, poll interval: %s", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.poll()
		case <-s.stopCh:
			s.logger.Println("Scheduler stopped")
			s.store.Close()
			return
		}
	}
}

func (s *Scheduler) Stop() {
	close(s.stopCh)
}

func (s *Scheduler) poll() {
	// Check scheduled (cron) tasks first
	scheduled, err := s.store.DueScheduled()
	if err != nil {
		s.logger.Printf("Error listing scheduled tasks: %v", err)
	}

	for _, task := range scheduled {
		if s.shouldRun(task) {
			s.execute(task)
			return // max_concurrent = 1
		}
	}

	// Check queued one-shot tasks
	queued, err := s.store.NextQueued()
	if err != nil {
		s.logger.Printf("Error getting next queued task: %v", err)
		return
	}

	if queued != nil {
		s.execute(queued)
	}
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

func (s *Scheduler) execute(task *Task) {
	s.logger.Printf("Executing task %s: %s %q", task.ID[:8], task.Type, task.Topic)

	if err := s.store.UpdateStatus(task.ID, StatusRunning); err != nil {
		s.logger.Printf("Error updating status: %v", err)
		return
	}

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

	// For recurring tasks, reset to scheduled; for one-shots, mark done
	if task.Cron != nil {
		s.store.UpdateStatus(task.ID, StatusScheduled)
		now := time.Now()
		task.LastRunAt = &now
	} else {
		s.store.UpdateStatus(task.ID, StatusDone)
	}

	s.logger.Printf("Task %s completed: %s", task.ID[:8], result.FilePath)
}
