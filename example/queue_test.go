package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type qPayload struct {
	Name string
	N    int
}

func newQueuePanel(t *testing.T, cfg steward.Config, register func(*steward.Panel)) (*steward.Panel, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	cfg.DB = db
	cfg.SecretKey = []byte("queue-test-secret-key-00000")
	app, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	register(app)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, db
}

func jobByID(t *testing.T, db *gorm.DB, id uint) steward.QueuedJob {
	t.Helper()
	var j steward.QueuedJob
	if err := db.First(&j, id).Error; err != nil {
		t.Fatal(err)
	}
	return j
}

func TestQueueRunsAJobWithItsPayload(t *testing.T) {
	var got qPayload
	var tenant uint
	var jt *steward.JobType[qPayload]
	app, db := newQueuePanel(t, steward.Config{}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "greet", func(ctx context.Context, p qPayload) error {
			got = p
			tenant, _ = steward.TenantFromContext(ctx)
			if job, ok := steward.JobFromContext(ctx); !ok || job.Attempts != 1 {
				return errors.New("the handler cannot see its job")
			}
			return nil
		})
	})
	job, err := jt.Enqueue(steward.WithTenant(context.Background(), 7), qPayload{Name: "Ana", N: 3})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := app.RunQueuedJobs(context.Background()); err != nil || n != 1 {
		t.Fatalf("RunQueuedJobs = %d, %v", n, err)
	}
	if got != (qPayload{Name: "Ana", N: 3}) {
		t.Errorf("payload = %+v", got)
	}
	if tenant != 7 {
		t.Errorf("the job ran in tenant %d, want the one it was queued in", tenant)
	}
	if j := jobByID(t, db, job.ID); j.Status != steward.JobDone || j.FinishedAt == nil {
		t.Errorf("job after a success = %+v", j)
	}
}

func TestQueueRetriesThenFails(t *testing.T) {
	var calls atomic.Int32
	var jt *steward.JobType[qPayload]
	app, db := newQueuePanel(t, steward.Config{}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "flaky", func(context.Context, qPayload) error {
			if calls.Add(1) == 2 {
				panic("second attempt explodes")
			}
			return errors.New("not yet")
		}).Retries(2)
	})
	job, err := jt.Enqueue(context.Background(), qPayload{})
	if err != nil {
		t.Fatal(err)
	}
	due := func() {
		// Skip the backoff: make the retry due now.
		db.Model(&steward.QueuedJob{}).Where("id = ?", job.ID).Update("run_at", time.Now().Add(-time.Second))
	}

	if _, err := app.RunQueuedJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	j := jobByID(t, db, job.ID)
	if j.Status != steward.JobPending || j.Attempts != 1 || !j.RunAt.After(time.Now()) || j.LastError != "not yet" {
		t.Fatalf("after a failed attempt with retries left: %+v", j)
	}
	// Not due yet, so another pass does nothing.
	if n, _ := app.RunQueuedJobs(context.Background()); n != 0 {
		t.Errorf("a job waiting out its backoff ran early")
	}

	due()
	if _, err := app.RunQueuedJobs(context.Background()); err != nil {
		t.Fatalf("a panicking job stopped the runner: %v", err)
	}
	if j := jobByID(t, db, job.ID); j.Status != steward.JobPending || !strings.Contains(j.LastError, "second attempt explodes") {
		t.Fatalf("a panic should fail the attempt like an error: %+v", j)
	}

	due()
	_, _ = app.RunQueuedJobs(context.Background())
	if j := jobByID(t, db, job.ID); j.Status != steward.JobFailed || j.Attempts != 3 || j.FinishedAt == nil {
		t.Fatalf("after the last attempt: %+v", j)
	}

	if err := app.RetryJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if j := jobByID(t, db, job.ID); j.Status != steward.JobPending || j.Attempts != 0 {
		t.Errorf("a retried job should be pending with its attempts reset: %+v", j)
	}
}

func TestQueueReleasesStaleClaims(t *testing.T) {
	ran := false
	var jt *steward.JobType[qPayload]
	app, db := newQueuePanel(t, steward.Config{}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "stuck", func(context.Context, qPayload) error {
			ran = true
			return nil
		}).Timeout(time.Minute)
	})
	job, _ := jt.Enqueue(context.Background(), qPayload{})
	// A worker claimed it and died: running, locked long ago.
	db.Model(&steward.QueuedJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"status": steward.JobRunning, "locked_at": time.Now().Add(-time.Hour), "attempts": 1,
	})
	if _, err := app.RunQueuedJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ran || jobByID(t, db, job.ID).Status != steward.JobDone {
		t.Error("a claim left behind by a dead worker should be released and run")
	}

	// A recent claim belongs to a live worker and is left alone.
	ran = false
	job2, _ := jt.Enqueue(context.Background(), qPayload{})
	db.Model(&steward.QueuedJob{}).Where("id = ?", job2.ID).Updates(map[string]any{
		"status": steward.JobRunning, "locked_at": time.Now(),
	})
	_, _ = app.RunQueuedJobs(context.Background())
	if ran {
		t.Error("a job another worker is running was run again")
	}
}

func TestQueueWorkersInPanel(t *testing.T) {
	done := make(chan qPayload, 1)
	var jt *steward.JobType[qPayload]
	_, _ = newQueuePanel(t, steward.Config{QueueWorkers: 1}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "ping", func(_ context.Context, p qPayload) error {
			done <- p
			return nil
		})
	})
	if _, err := jt.Enqueue(context.Background(), qPayload{Name: "now"}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-done:
		if p.Name != "now" {
			t.Errorf("payload = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panel's own worker did not run a queued job")
	}
}

func TestJobsAdminPage(t *testing.T) {
	var jt *steward.JobType[qPayload]
	app, db := newQueuePanel(t, steward.Config{Prefix: "/admin", AuthExcept: []string{"/auth/jobs*"}}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "report", func(context.Context, qPayload) error { return errors.New("boom") })
	})
	job, _ := jt.Enqueue(context.Background(), qPayload{})
	_, _ = app.RunQueuedJobs(context.Background())

	srv := serve(t, app)
	t.Cleanup(srv.Close)
	c := newNuClient(t, srv.URL+"/admin/auth/jobs")
	page := c.get("")
	if !strings.Contains(page, "report") || !strings.Contains(page, "boom") {
		t.Error("the jobs page should list the job and its error")
	}
	m := comboCSRFRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token")
	}
	c.csrf = m[1]
	code, body := c.send("POST", "/_action/retry", "application/x-www-form-urlencoded",
		strings.NewReader("ids="+strconv.FormatUint(uint64(job.ID), 10)))
	if code != 200 || !strings.Contains(body, "queued again") {
		t.Fatalf("retry = %d %s", code, body)
	}
	if j := jobByID(t, db, job.ID); j.Status != steward.JobPending {
		t.Errorf("retry left the job %s", j.Status)
	}
}

// Several workers polling one table run each job once: the claim is a
// conditional update, and only one of them can win it.
func TestQueueWorkersRunEachJobOnce(t *testing.T) {
	const jobs = 30
	var mu sync.Mutex
	seen := map[int]int{}
	var jt *steward.JobType[qPayload]
	app, db := newQueuePanel(t, steward.Config{}, func(a *steward.Panel) {
		jt = steward.RegisterJob(a, "count", func(_ context.Context, p qPayload) error {
			mu.Lock()
			seen[p.N]++
			mu.Unlock()
			return nil
		})
	})
	for i := range jobs {
		if _, err := jt.Enqueue(context.Background(), qPayload{N: i}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { app.WorkQueue(ctx, 4); close(stopped) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var left int64
		db.Model(&steward.QueuedJob{}).Where("status <> ?", steward.JobDone).Count(&left)
		if left == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-stopped

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != jobs {
		t.Errorf("%d of %d jobs ran", len(seen), jobs)
	}
	for n, times := range seen {
		if times != 1 {
			t.Errorf("job %d ran %d times", n, times)
		}
	}
}
