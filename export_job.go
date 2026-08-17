package steward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ExportJob is one requested CSV, built away from the request that asked for
// it. A whole-table export of any size holds a connection open for as long as
// it takes to write, and every proxy in front of the panel has an opinion about
// how long that may be; past a threshold the panel takes the request, answers
// at once, and notifies the account when the file is ready.
type ExportJob struct {
	ID     uint   `gorm:"primaryKey"`
	UserID uint   `gorm:"not null;index"`
	Slug   string `gorm:"size:120;not null"`

	// Query is the grid's own query string, so the export covers exactly what
	// the reader was looking at: filters, quick search, and sort.
	Query string `gorm:"type:text"`

	// Status is one of pending, running, done, failed. Indexed because the
	// runner's only question is "is there a pending one".
	Status string `gorm:"size:16;not null;index"`

	Rows  int64 `gorm:"column:row_count"`
	Bytes int64
	Disk  string `gorm:"size:60"`
	Path  string `gorm:"size:512"`
	Err   string `gorm:"type:text"`

	CreatedAt time.Time
	StartedAt *time.Time
	DoneAt    *time.Time
}

func (ExportJob) TableName() string { return prefixed("exports") }

// Export status values.
const (
	ExportPending = "pending"
	ExportRunning = "running"
	ExportDone    = "done"
	ExportFailed  = "failed"
)

// defaultBackgroundExportRows is the match size past which a whole-table export
// stops being a download and becomes a job. Measured against a table of 102k
// articles: 10k rows write in about six seconds, which a request survives
// everywhere; the whole table took over a minute, which it does not.
const defaultBackgroundExportRows = 10000

// queuedExport is what the grid answers with when it decided not to stream.
type queuedExport struct {
	Job     *ExportJob
	Message string
}

// csvExporter is the type-erased half of a resource that can be exported. It is
// not on resourceEntry because a resource whose grid has export turned off
// cannot do this, and the interface describes what all of them do.
type csvExporter interface {
	exportRows(c *Context, out *bytes.Buffer, query url.Values) (int64, error)
	countExport(c *Context, query url.Values) (int64, error)
}

// backgroundExportRows is the configured threshold, or the default.
func (a *Admin) backgroundExportRows() int64 {
	switch {
	case a.cfg.BackgroundExportRows < 0:
		return 0 // never in the background
	case a.cfg.BackgroundExportRows == 0:
		return defaultBackgroundExportRows
	default:
		return int64(a.cfg.BackgroundExportRows)
	}
}

// maybeQueueExport decides whether this export is a download or a job, and
// creates the job if it is. A nil result means "stream it".
func (a *Admin) maybeQueueExport(c *Context, slug string, st *gridState) (*queuedExport, error) {
	limit := a.backgroundExportRows()
	if limit == 0 || c.User == nil {
		return nil, nil
	}
	res, ok := a.bySlug[slug]
	if !ok {
		return nil, nil
	}
	ex, ok := res.(csvExporter)
	if !ok {
		return nil, nil
	}

	// A selection is bounded by the ids in hand and never worth a job.
	if st.export == "selected" {
		return nil, nil
	}
	n, err := ex.countExport(c, c.R.URL.Query())
	if err != nil {
		return nil, err
	}
	if n < limit {
		return nil, nil
	}

	job := ExportJob{
		UserID: c.User.ID,
		Slug:   slug,
		Query:  c.R.URL.RawQuery,
		Status: ExportPending,
	}
	if err := a.db.WithContext(c.Ctx()).Create(&job).Error; err != nil {
		return nil, fmt.Errorf("queueing the export: %w", err)
	}
	a.wakeExports()
	return &queuedExport{
		Job: &job,
		Message: fmt.Sprintf("Preparing %s rows in the background — you will be notified when the file is ready.",
			formatThousands(n)),
	}, nil
}

// Exports returns an account's export jobs, newest first.
func (a *Admin) Exports(ctx context.Context, userID uint, limit int) ([]ExportJob, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []ExportJob
	err := a.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("id DESC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// RunPendingExports builds every queued export, oldest first, and returns how
// many it completed.
//
// The panel runs this itself unless DisableExportWorker is set; call it from a
// worker's scheduler instead when the panel should not do the work. Claiming is
// a conditional update, so several processes may run it at once without two of
// them building the same file.
func (a *Admin) RunPendingExports(ctx context.Context) (int, error) {
	var done int
	for {
		job, ok, err := a.claimExport(ctx)
		if err != nil {
			return done, err
		}
		if !ok {
			return done, nil
		}
		if err := a.runExport(ctx, job); err != nil {
			a.failExport(ctx, job, err)
			a.log.Error("steward: export failed", "job", job.ID, "slug", job.Slug, "err", err)
			continue
		}
		done++
	}
}

// claimExport takes the oldest pending job for this process. The UPDATE's
// affected-row count is the claim: whoever changes the row owns it.
func (a *Admin) claimExport(ctx context.Context) (*ExportJob, bool, error) {
	db := a.db.WithContext(ctx)
	for {
		// Find rather than First: an empty queue is the normal state, and First
		// reports it as ErrRecordNotFound, which GORM's logger writes at error
		// level — an error line a minute on a panel with nothing to export.
		var found []ExportJob
		if err := db.Where("status = ?", ExportPending).
			Order("id ASC").Limit(1).Find(&found).Error; err != nil {
			return nil, false, err
		}
		if len(found) == 0 {
			return nil, false, nil
		}
		job := found[0]
		now := time.Now()
		res := db.Model(&ExportJob{}).
			Where("id = ? AND status = ?", job.ID, ExportPending).
			Updates(map[string]any{"status": ExportRunning, "started_at": now})
		if res.Error != nil {
			return nil, false, res.Error
		}
		if res.RowsAffected == 1 {
			job.Status, job.StartedAt = ExportRunning, &now
			return &job, true, nil
		}
		// Someone else claimed it between the read and the update; look again.
	}
}

// runExport builds one job's file and notifies its owner.
func (a *Admin) runExport(ctx context.Context, job *ExportJob) error {
	res, ok := a.bySlug[job.Slug]
	if !ok {
		return fmt.Errorf("no resource named %q", job.Slug)
	}
	ex, ok := res.(csvExporter)
	if !ok {
		return fmt.Errorf("resource %q cannot be exported", job.Slug)
	}

	var user AdminUser
	if err := a.db.WithContext(ctx).Preload("Roles").First(&user, job.UserID).Error; err != nil {
		return fmt.Errorf("loading the account that asked: %w", err)
	}

	// A request the job can be read from: the grid's state comes out of the
	// query string, and the row scope out of the user. Nothing writes to a
	// response, so there is none.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/?"+job.Query, nil)
	if err != nil {
		return err
	}
	c := &Context{R: req, Admin: a, User: &user}

	var buf bytes.Buffer
	rows, err := ex.exportRows(c, &buf, req.URL.Query())
	if err != nil {
		return err
	}

	disk := a.cfg.DefaultDisk
	name := fmt.Sprintf("exports/%s-%d-%s.csv", job.Slug, job.ID, time.Now().UTC().Format("20060102-150405"))
	stored, err := a.storeExport(ctx, disk, name, buf.Bytes())
	if err != nil {
		return err
	}

	now := time.Now()
	err = a.db.WithContext(ctx).Model(&ExportJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"status": ExportDone, "row_count": rows, "bytes": int64(buf.Len()),
			"disk": disk, "path": stored, "done_at": now, "err": "",
		}).Error
	if err != nil {
		return err
	}

	if a.notificationsEnabled() {
		title := job.Slug + " export ready"
		if m := res.meta(); m != nil && m.title != "" {
			title = m.title + " export ready"
		}
		n := Notification{
			Title: title,
			Body:  fmt.Sprintf("%s rows, %s.", formatThousands(rows), formatBytes(int64(buf.Len()))),
			URL:   a.url("_exports", strconv.FormatUint(uint64(job.ID), 10), "download"),
			Icon:  "download",
			Type:  "export.ready",
		}
		if err := a.Notify(ctx, job.UserID, n); err != nil {
			// The file is built; failing the job over the notification would
			// rebuild it on the next pass.
			a.log.Error("steward: export notification", "job", job.ID, "err", err)
		}
	}
	return nil
}

func (a *Admin) failExport(ctx context.Context, job *ExportJob, cause error) {
	now := time.Now()
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if err := a.db.WithContext(ctx).Model(&ExportJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{"status": ExportFailed, "err": msg, "done_at": now}).Error; err != nil {
		a.log.Error("steward: recording an export failure", "job", job.ID, "err", err)
		return
	}
	if a.notificationsEnabled() {
		_ = a.Notify(ctx, job.UserID, Notification{
			Title: "Export failed",
			Body:  msg,
			Icon:  "circle-alert",
			Type:  "export.failed",
		})
	}
}

// PruneExports deletes finished jobs older than age, and the files they point
// at. Nothing calls it for you.
func (a *Admin) PruneExports(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, errors.New("steward: PruneExports needs a positive age")
	}
	cutoff := time.Now().Add(-age)
	var jobs []ExportJob
	err := a.db.WithContext(ctx).
		Where("status IN ? AND created_at < ?", []string{ExportDone, ExportFailed}, cutoff).
		Find(&jobs).Error
	if err != nil {
		return 0, err
	}
	var gone int64
	for i := range jobs {
		job := &jobs[i]
		if job.Path != "" {
			if st, ok := a.Disk(job.Disk); ok && st.Storage != nil {
				if err := st.Storage.Delete(ctx, job.Path); err != nil {
					a.log.Error("steward: deleting an export file", "job", job.ID, "err", err)
				}
			}
		}
		if err := a.db.WithContext(ctx).Delete(&ExportJob{}, job.ID).Error; err != nil {
			return gone, err
		}
		gone++
	}
	return gone, nil
}

// formatThousands groups digits so a row count reads at a glance.
func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// storeExport writes the built file to a disk and returns the stored name.
func (a *Admin) storeExport(ctx context.Context, disk, name string, body []byte) (string, error) {
	d, ok := a.Disk(disk)
	if !ok || d.Storage == nil {
		return "", fmt.Errorf("no storage for disk %q", disk)
	}
	if _, err := d.Storage.Put(ctx, name, bytes.NewReader(body), int64(len(body)), "text/csv; charset=utf-8"); err != nil {
		return "", fmt.Errorf("storing the export: %w", err)
	}
	return name, nil
}

// startExportWorker runs queued exports inside the panel process.
//
// A panel is usually its own binary, and requiring a second process before an
// export completes would make the feature look broken. One job at a time, woken
// by a new request and otherwise polled slowly, so an export queued by another
// replica is still picked up.
func (a *Admin) startExportWorker() {
	if a.cfg.DisableExportWorker {
		return
	}
	a.exportWake = make(chan struct{}, 1)
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			if _, err := a.RunPendingExports(context.Background()); err != nil {
				a.log.Error("steward: running queued exports", "err", err)
			}
			select {
			case <-a.exportWake:
			case <-tick.C:
			}
		}
	}()
}

// wakeExports nudges the worker so a queued export starts at once rather than
// on the next tick. Never blocks: a full channel already means "there is work".
func (a *Admin) wakeExports() {
	if a.exportWake == nil {
		return
	}
	select {
	case a.exportWake <- struct{}{}:
	default:
	}
}

// downloadExport serves a finished export to the account that asked for it.
func (a *Admin) downloadExport(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	id, err := strconv.ParseUint(c.R.PathValue("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, Error("Unknown export."))
	}
	var job ExportJob
	// The account is part of the statement: an export belongs to whoever asked
	// for it, and its rows are whatever their policies allowed them to read.
	if err := a.db.WithContext(c.Ctx()).
		Where("id = ? AND user_id = ?", uint(id), c.User.ID).
		First(&job).Error; err != nil {
		a.renderError(c, http.StatusNotFound, "Export not found", nil)
		return nil
	}
	if job.Status != ExportDone || job.Path == "" {
		a.renderError(c, http.StatusNotFound, "That export is not ready", nil)
		return nil
	}
	d, ok := a.Disk(job.Disk)
	if !ok || d.Storage == nil {
		return fmt.Errorf("no storage for disk %q", job.Disk)
	}
	ls, ok := d.Storage.(*LocalStorage)
	if !ok {
		// An object store hands out its own URL; a private one signs it.
		return c.Redirect(d.Storage.URL(job.Path))
	}

	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+job.Slug+`.csv"`)
	c.W.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(c.W, c.R, filepath.Join(ls.Dir, filepath.FromSlash(job.Path)))
	return nil
}
