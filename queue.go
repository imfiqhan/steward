package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"runtime/debug"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// QueuedJob is one unit of background work, stored so that whichever process
// runs the queue — the `worker` command, or the panel with QueueWorkers set —
// can pick it up, and so that it survives a restart.
type QueuedJob struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"size:120;not null;index"`
	Payload string `gorm:"type:text"`
	// Status is one of pending, running, done, failed.
	Status      string    `gorm:"size:16;not null;index"`
	Attempts    int       `gorm:"not null;default:0"`
	MaxAttempts int       `gorm:"not null;default:1"`
	RunAt       time.Time `gorm:"not null;index"`
	LockedAt    *time.Time
	// TenantID and UserID are who queued it: the handler runs inside that
	// tenant, and UserID is there for the handler to read.
	TenantID   uint   `gorm:"index;default:0"`
	UserID     uint   `gorm:"default:0"`
	LastError  string `gorm:"type:text"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

func (QueuedJob) TableName() string { return prefixed("jobs") }

// Queued job status values.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

const (
	// defaultJobTimeout bounds one attempt of a job type that sets none.
	defaultJobTimeout = 5 * time.Minute
	// jobPoll is how often an idle queue worker looks for work queued by
	// another process. Work queued by this one wakes it at once.
	jobPoll = 2 * time.Second
	// maxJobBackoff caps the wait before a retry.
	maxJobBackoff = time.Hour
)

// jobRunner is a JobType behind the panel's registry, which cannot name P.
type jobRunner interface {
	run(ctx context.Context, payload string) error
	attempts() int
	timeout() time.Duration
}

// JobType is a kind of background work with a typed payload, registered with
// RegisterJob.
type JobType[P any] struct {
	a           *Panel
	name        string
	fn          func(ctx context.Context, payload P) error
	maxAttempts int
	limit       time.Duration
}

// RegisterJob declares a kind of background work. fn receives the payload it
// was queued with, decoded from JSON, and a context carrying the tenant it was
// queued in; JobFromContext reads the job itself. An error, or a panic, fails
// the attempt.
//
//	resize := steward.RegisterJob(app, "resize-cover", func(ctx context.Context, p ResizeCover) error {
//	    return images.Resize(ctx, p.Path, 1200)
//	}).Retries(3)
//
//	// in a handler, a hook, or anywhere with a context:
//	resize.Dispatch(c, ResizeCover{Path: post.Cover})
//
// Register every job type before Build, like resources. The queue is processed
// by the `worker` command, or by the panel itself with Config.QueueWorkers.
func RegisterJob[P any](a *Panel, name string, fn func(ctx context.Context, payload P) error) *JobType[P] {
	if a.built {
		panic("steward: RegisterJob called after Build — register job types first")
	}
	if _, dup := a.jobTypes[name]; dup {
		panic(fmt.Sprintf("steward: job type %q registered twice", name))
	}
	jt := &JobType[P]{a: a, name: name, fn: fn, maxAttempts: 1, limit: defaultJobTimeout}
	a.jobTypes[name] = jt
	return jt
}

// Retries lets a failed attempt run again, up to n more times, waiting longer
// before each: 10s, 20s, 40s and so on, at most an hour.
func (j *JobType[P]) Retries(n int) *JobType[P] {
	j.maxAttempts = max(n, 0) + 1
	return j
}

// Timeout bounds one attempt; its context is cancelled when it runs out. The
// default is five minutes.
func (j *JobType[P]) Timeout(d time.Duration) *JobType[P] {
	if d > 0 {
		j.limit = d
	}
	return j
}

// Name is the type's registered name, as stored on its jobs.
func (j *JobType[P]) Name() string { return j.name }

func (j *JobType[P]) attempts() int          { return j.maxAttempts }
func (j *JobType[P]) timeout() time.Duration { return j.limit }

func (j *JobType[P]) run(ctx context.Context, payload string) error {
	var p P
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return fmt.Errorf("decoding the payload: %w", err)
	}
	return j.fn(ctx, p)
}

// Enqueue queues one job to run as soon as a worker is free. The tenant is
// read from ctx, so a job queued while serving a workspace runs in it.
func (j *JobType[P]) Enqueue(ctx context.Context, payload P) (*QueuedJob, error) {
	return j.enqueue(ctx, time.Now(), 0, payload)
}

// EnqueueAt queues one job to run no earlier than at.
func (j *JobType[P]) EnqueueAt(ctx context.Context, at time.Time, payload P) (*QueuedJob, error) {
	return j.enqueue(ctx, at, 0, payload)
}

// Dispatch queues one job from a request, recording the signed-in account as
// the job's UserID along with the request's tenant.
func (j *JobType[P]) Dispatch(c *Context, payload P) (*QueuedJob, error) {
	var uid uint
	if c.User != nil {
		uid = c.User.ID
	}
	return j.enqueue(c.Ctx(), time.Now(), uid, payload)
}

func (j *JobType[P]) enqueue(ctx context.Context, at time.Time, userID uint, payload P) (*QueuedJob, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("steward: encoding the %s payload: %w", j.name, err)
	}
	tenant, _ := TenantFromContext(ctx)
	job := &QueuedJob{
		Name: j.name, Payload: string(raw), Status: JobPending,
		MaxAttempts: j.maxAttempts, RunAt: at, TenantID: tenant, UserID: userID,
	}
	if err := j.a.db.WithContext(WithoutTenant(ctx)).Create(job).Error; err != nil {
		return nil, fmt.Errorf("steward: queueing %s: %w", j.name, err)
	}
	j.a.wakeQueue()
	return job, nil
}

type jobCtxKey struct{}

// JobFromContext returns the job a handler is running, for its ID, attempt
// count or UserID.
func JobFromContext(ctx context.Context) (*QueuedJob, bool) {
	job, ok := ctx.Value(jobCtxKey{}).(*QueuedJob)
	return job, ok
}

// jobNames lists the registered types, which are the only jobs this process
// can run. A job of a type only another binary knows is left for that binary.
func (a *Panel) jobNames() []string {
	out := make([]string, 0, len(a.jobTypes))
	for name := range a.jobTypes {
		out = append(out, name)
	}
	return out
}

// staleAfter is how long a running job may hold its claim before it is taken
// to belong to a process that died. Longer than any type's timeout, so a slow
// attempt is never run twice at once.
func (a *Panel) staleAfter() time.Duration {
	longest := defaultJobTimeout
	for _, jt := range a.jobTypes {
		longest = max(longest, jt.timeout())
	}
	return 2 * longest
}

// releaseStaleJobs returns claims older than staleAfter to the queue.
func (a *Panel) releaseStaleJobs(ctx context.Context) error {
	cutoff := time.Now().Add(-a.staleAfter())
	return a.db.WithContext(ctx).Model(&QueuedJob{}).
		Where("status = ? AND locked_at < ?", JobRunning, cutoff).
		Updates(map[string]any{"status": JobPending, "locked_at": nil}).Error
}

// claimJob takes the next due job of a type this process knows. The UPDATE's
// affected-row count is the claim, so several workers in several processes can
// poll the same table without two of them running one job.
func (a *Panel) claimJob(ctx context.Context) (*QueuedJob, bool, error) {
	names := a.jobNames()
	if len(names) == 0 {
		return nil, false, nil
	}
	db := a.db.WithContext(ctx)
	for {
		var found []QueuedJob
		if err := db.Where("status = ? AND run_at <= ? AND name IN ?", JobPending, time.Now(), names).
			Order("run_at ASC").Order("id ASC").Limit(1).Find(&found).Error; err != nil {
			return nil, false, err
		}
		if len(found) == 0 {
			return nil, false, nil
		}
		job := found[0]
		now := time.Now()
		res := db.Model(&QueuedJob{}).
			Where("id = ? AND status = ?", job.ID, JobPending).
			Updates(map[string]any{
				"status": JobRunning, "locked_at": now, "attempts": gorm.Expr("attempts + 1"),
			})
		if res.Error != nil {
			return nil, false, res.Error
		}
		if res.RowsAffected == 1 {
			job.Status, job.LockedAt, job.Attempts = JobRunning, &now, job.Attempts+1
			return &job, true, nil
		}
	}
}

// runJob runs one claimed attempt and records the outcome.
func (a *Panel) runJob(ctx context.Context, job *QueuedJob) error {
	jt := a.jobTypes[job.Name]
	err := a.attemptJob(ctx, jt, job)
	now := time.Now()
	db := a.db.WithContext(ctx).Model(&QueuedJob{}).Where("id = ?", job.ID)
	if err == nil {
		return db.Updates(map[string]any{
			"status": JobDone, "locked_at": nil, "finished_at": now, "last_error": "",
		}).Error
	}
	msg := err.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	a.log.Error("steward: job failed", "job", job.ID, "name", job.Name, "attempt", job.Attempts, "err", err)
	if job.Attempts < job.MaxAttempts {
		return db.Updates(map[string]any{
			"status": JobPending, "locked_at": nil, "last_error": msg,
			"run_at": now.Add(jobBackoff(job.Attempts)),
		}).Error
	}
	return db.Updates(map[string]any{
		"status": JobFailed, "locked_at": nil, "last_error": msg, "finished_at": now,
	}).Error
}

// attemptJob calls the handler under the job's tenant and timeout, turning a
// panic into the attempt's error so one bad job cannot stop the worker.
func (a *Panel) attemptJob(ctx context.Context, jt jobRunner, job *QueuedJob) (err error) {
	if jt == nil {
		return fmt.Errorf("no job type %q is registered", job.Name)
	}
	if job.TenantID != 0 {
		ctx = WithTenant(ctx, job.TenantID)
	} else {
		ctx = WithoutTenant(ctx)
	}
	ctx = context.WithValue(ctx, jobCtxKey{}, job)
	ctx, cancel := context.WithTimeout(ctx, jt.timeout())
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			// The stack goes to the log; the job keeps the message, which is
			// what the admin page has room to show.
			a.log.Error("steward: job panicked", "job", job.ID, "name", job.Name,
				"panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return jt.run(ctx, job.Payload)
}

// jobBackoff is the wait before attempt n+1: 10s doubling, capped.
func jobBackoff(attempt int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempt && d < maxJobBackoff; i++ {
		d *= 2
	}
	return min(d, maxJobBackoff)
}

// RunQueuedJobs runs every job that is due now, one at a time, and returns how
// many attempts it made. A job that fails and may retry is due again later, not
// during this call.
//
// The `worker` command and Config.QueueWorkers call it in a loop; call it
// yourself from a test, or from a scheduler of your own.
func (a *Panel) RunQueuedJobs(ctx context.Context) (int, error) {
	if err := a.Build(); err != nil {
		return 0, err
	}
	ctx = WithoutTenant(ctx)
	if err := a.releaseStaleJobs(ctx); err != nil {
		return 0, err
	}
	n := 0
	for ctx.Err() == nil {
		job, ok, err := a.claimJob(ctx)
		if err != nil {
			return n, err
		}
		if !ok {
			return n, nil
		}
		n++
		if err := a.runJob(ctx, job); err != nil {
			return n, err
		}
	}
	return n, nil
}

// WorkQueue processes the queue with the given number of workers until ctx is
// done, polling for jobs queued by other processes and waking at once for
// jobs queued by this one.
func (a *Panel) WorkQueue(ctx context.Context, workers int) {
	workers = max(workers, 1)
	done := make(chan struct{})
	for range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			tick := time.NewTicker(jobPoll)
			defer tick.Stop()
			for ctx.Err() == nil {
				if _, err := a.RunQueuedJobs(ctx); err != nil && !errors.Is(err, context.Canceled) {
					a.log.Error("steward: running queued jobs", "err", err)
				}
				select {
				case <-ctx.Done():
				case <-a.queueWake:
				case <-tick.C:
				}
			}
		}()
	}
	for range workers {
		<-done
	}
}

// wakeQueue nudges an idle worker in this process. It never blocks: a worker
// already awake will see the job on its next pass.
func (a *Panel) wakeQueue() {
	select {
	case a.queueWake <- struct{}{}:
	default:
	}
}

// startQueueWorkers runs Config.QueueWorkers workers inside the panel process
// until Close.
func (a *Panel) startQueueWorkers() {
	if a.cfg.QueueWorkers <= 0 || len(a.jobTypes) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.queueStop = cancel
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		a.WorkQueue(ctx, a.cfg.QueueWorkers)
	}()
}

// RetryJob puts a finished or failed job back in the queue, due now, with its
// attempts reset.
func (a *Panel) RetryJob(ctx context.Context, id uint) error {
	res := a.db.WithContext(WithoutTenant(ctx)).Model(&QueuedJob{}).
		Where("id = ? AND status <> ?", id, JobRunning).
		Updates(map[string]any{
			"status": JobPending, "attempts": 0, "run_at": time.Now(),
			"locked_at": nil, "finished_at": nil, "last_error": "",
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("job %d is running or does not exist", id)
	}
	a.wakeQueue()
	return nil
}

// PruneJobs deletes done and failed jobs that finished more than age ago.
// Nothing calls it for you; a scheduled job is the usual place.
func (a *Panel) PruneJobs(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, errors.New("steward: PruneJobs needs a positive age")
	}
	res := a.db.WithContext(WithoutTenant(ctx)).
		Where("status IN ? AND finished_at < ?", []string{JobDone, JobFailed}, time.Now().Add(-age)).
		Delete(&QueuedJob{})
	return res.RowsAffected, res.Error
}

// registerJobsResource is the admin page over the queue, mounted when any job
// type is registered.
func (a *Panel) registerJobsResource() {
	if len(a.jobTypes) == 0 {
		return
	}
	res := Register[QueuedJob](a).Slug("auth/jobs").Title("Jobs").
		Icon("list-checks").Group("Admin")
	if a.TenancyEnabled() {
		res.Policy(platformOnly[QueuedJob]{})
	}
	retry := func(c *Context, ids []string) (*Envelope, error) {
		n := 0
		for _, raw := range ids {
			id, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				continue
			}
			if err := c.Panel.RetryJob(c.Ctx(), uint(id)); err == nil {
				n++
			}
		}
		if n == 0 {
			return Error("Nothing was queued again: a running job cannot be retried."), nil
		}
		return Success(c.T("%d queued again.", n)).Refresh(), nil
	}
	statusColours := map[any]BadgeColor{
		JobPending: BadgeSecondary, JobRunning: BadgeBlue, JobDone: BadgeGreen, JobFailed: BadgeRed,
	}
	res.Grid(func(g *Grid[QueuedJob]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Name").Sortable()
		g.Column("Status").Badge(statusColours)
		g.ColumnFunc("Tries", "Attempts", func(j *QueuedJob) template.HTML {
			return template.HTML(strconv.Itoa(j.Attempts) + " / " + strconv.Itoa(j.MaxAttempts))
		})
		g.Column("RunAt", "Due").Sortable()
		g.Column("LastError", "Last error").Limit(60)
		g.Column("FinishedAt", "Finished").Sortable()
		g.DefaultSort("ID", true)
		g.QuickSearch("Name")
		g.Filter(func(f *Filters[QueuedJob]) {
			f.Equal("Status").Select(Options{
				JobPending: "Pending", JobRunning: "Running", JobDone: "Done", JobFailed: "Failed",
			})
			f.Equal("Name")
		})
		g.DisableCreate()
		g.DisableEdit()
		g.RowAction(NewAction("retry", "Retry", retry).Icon("rotate-ccw"))
		g.BatchAction(NewAction("retry-selected", "Retry selected", retry).Icon("rotate-ccw"))
	})
	res.Detail(func(d *Detail[QueuedJob]) {
		d.Field("ID")
		d.Field("Name")
		d.Field("Status").Badge(statusColours)
		d.Field("Attempts")
		d.Field("MaxAttempts", "Max attempts")
		d.Field("RunAt", "Due")
		d.Field("TenantID", "Tenant")
		d.Field("UserID", "Queued by")
		d.Field("Payload").JSON()
		d.Field("LastError", "Last error")
		d.Field("CreatedAt", "Queued")
		d.Field("FinishedAt", "Finished")
	})
}
