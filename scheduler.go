package steward

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IntervalScheduler is a minimal Scheduler running each job on a fixed
// interval. Specs: "@every 10m" (any time.ParseDuration string), "@hourly",
// "@daily", "@weekly". Full cron expressions are a documented non-goal for
// v1 — implement Scheduler over a cron library if you need them.
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
	fn       func(context.Context) error

	mu      sync.Mutex
	lastRun time.Time
	lastErr string
}

// NewIntervalScheduler returns a scheduler; Start it once jobs are added.
func NewIntervalScheduler() *IntervalScheduler { return &IntervalScheduler{} }

// Add implements Scheduler.
func (s *IntervalScheduler) Add(spec, name string, fn func(context.Context) error) error {
	d, err := parseSpec(spec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job := &intervalJob{name: name, spec: spec, interval: d, fn: fn}
	s.jobs = append(s.jobs, job)
	if s.started {
		go s.runJob(s.ctx, job)
	}
	return nil
}

func parseSpec(spec string) (time.Duration, error) {
	switch spec {
	case "@hourly":
		return time.Hour, nil
	case "@daily":
		return 24 * time.Hour, nil
	case "@weekly":
		return 7 * 24 * time.Hour, nil
	}
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		return time.ParseDuration(strings.TrimSpace(rest))
	}
	return 0, fmt.Errorf("steward: unsupported schedule %q (use @every <duration>, @hourly, @daily, @weekly)", spec)
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
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := j.fn(ctx)
			j.mu.Lock()
			j.lastRun = time.Now()
			j.lastErr = ""
			if err != nil {
				j.lastErr = err.Error()
			}
			j.mu.Unlock()
		}
	}
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

// schedulerPage renders the read-only jobs table when a Scheduler is
// configured; mounted at {prefix}/scheduler.
func (a *Admin) schedulerPage(c *Context) error {
	jobs := a.cfg.Scheduler.Jobs()
	return a.render(c, "pages/scheduler.html", "Scheduler", map[string]any{"Jobs": jobs})
}

func (a *Admin) registerSchedulerRoute(mux *http.ServeMux) {
	if a.cfg.Scheduler == nil {
		return
	}
	mux.HandleFunc("GET "+a.cfg.Prefix+"/scheduler", a.h(a.schedulerPage))
}
