package steward

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/imfiqhan/steward/internal/cron"
)

// IntervalScheduler runs jobs on intervals or cron expressions. Specs:
// "@every 10m" (any time.ParseDuration string), "@hourly", "@daily",
// "@weekly", or a standard five-field cron expression ("30 2 * * 1-5",
// minute resolution).
type IntervalScheduler struct {
	mu      sync.Mutex
	jobs    []*intervalJob
	started bool
	ctx     context.Context
}

type intervalJob struct {
	name     string
	spec     string
	interval time.Duration
	cron     *cron.Expr
	fn       func(context.Context) error

	mu      sync.Mutex
	lastRun time.Time
	lastErr string
}

// NewIntervalScheduler returns a scheduler; Start it once jobs are added.
func NewIntervalScheduler() *IntervalScheduler { return &IntervalScheduler{} }

// Add implements Scheduler.
func (s *IntervalScheduler) Add(spec, name string, fn func(context.Context) error) error {
	job := &intervalJob{name: name, spec: spec, fn: fn}
	if err := parseSpec(spec, job); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
	if s.started {
		go s.runJob(s.ctx, job)
	}
	return nil
}

func parseSpec(spec string, job *intervalJob) error {
	switch spec {
	case "@hourly":
		job.interval = time.Hour
		return nil
	case "@daily":
		job.interval = 24 * time.Hour
		return nil
	case "@weekly":
		job.interval = 7 * 24 * time.Hour
		return nil
	}
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return err
		}
		job.interval = d
		return nil
	}
	expr, err := cron.Parse(spec)
	if err != nil {
		return fmt.Errorf("steward: unsupported schedule %q (use @every <duration>, @hourly, @daily, @weekly, or a five-field cron expression): %w", spec, err)
	}
	job.cron = expr
	return nil
}

// Start launches all jobs until ctx is done.
func (s *IntervalScheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.ctx = ctx
	for _, j := range s.jobs {
		go s.runJob(ctx, j)
	}
}

func (s *IntervalScheduler) runJob(ctx context.Context, j *intervalJob) {
	if ctx == nil {
		ctx = context.Background()
	}
	if j.cron != nil {
		s.runCronJob(ctx, j)
		return
	}
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.execute(ctx)
		}
	}
}

// runCronJob wakes at each minute boundary and fires on matches.
func (s *IntervalScheduler) runCronJob(ctx context.Context, j *intervalJob) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			if j.cron.Matches(next) {
				j.execute(ctx)
			}
		}
	}
}

func (j *intervalJob) execute(ctx context.Context) {
	err := j.fn(ctx)
	j.mu.Lock()
	j.lastRun = time.Now()
	j.lastErr = ""
	if err != nil {
		j.lastErr = err.Error()
	}
	j.mu.Unlock()
}

// Jobs implements Scheduler.
func (s *IntervalScheduler) Jobs() []JobInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobInfo, 0, len(s.jobs))
	for _, j := range s.jobs {
		j.mu.Lock()
		out = append(out, JobInfo{Name: j.name, Spec: j.spec, LastRun: j.lastRun, LastErr: j.lastErr})
		j.mu.Unlock()
	}
	return out
}
