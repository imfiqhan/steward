package steward

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ImportRun is one CSV import: the uploaded file, how its columns map onto the
// form, and once it has run, what it made of each row.
type ImportRun struct {
	ID       uint   `gorm:"primaryKey"`
	UserID   uint   `gorm:"not null;index"`
	TenantID uint   `gorm:"index;default:0"`
	Slug     string `gorm:"size:120;not null"`
	// Status is one of the Import constants.
	Status   string `gorm:"size:16;not null;index"`
	FileName string `gorm:"size:255"`
	// Data is the uploaded file. It lives in the row rather than on a disk
	// because the worker that runs the import need not share the panel's
	// disk; it is cleared when the run finishes.
	Data []byte `gorm:"size:33554432"`
	// Mapping is a JSON array, one field path per CSV column, "" to skip.
	Mapping    string `gorm:"type:text"`
	Total      int
	Created    int
	Updated    int
	Failed     int
	Disk       string `gorm:"size:60"`
	ReportPath string `gorm:"size:512"`
	Err        string `gorm:"type:text"`
	CreatedAt  time.Time
	FinishedAt *time.Time
}

func (ImportRun) TableName() string { return prefixed("import_runs") }

// Import run status values.
const (
	ImportMapping = "mapping"
	ImportPending = "pending"
	ImportRunning = "running"
	ImportDone    = "done"
	ImportFailed  = "failed"
)

const (
	// importMaxBytes bounds an uploaded CSV.
	importMaxBytes = 10 << 20
	// importJobName is the queue's name for an import run.
	importJobName = "steward.import"
	// importSampleRows is how many data rows the mapping page shows per column.
	importSampleRows = 3
)

// Import adds an Import button to the grid: upload a CSV, match its columns to
// the form's fields, and the rows are created — or, where a column holds the
// record key, updated — through the form, with its rules, hooks and policy.
// The run happens on the job queue and the account is notified when it ends,
// with a report of the rows that failed and why.
//
// A file exported from the same grid imports back: its headers are the
// column labels, which match the form's labels. Uploads, multi-selects,
// embedded values and repeaters cannot come from a CSV cell and are not
// offered.
func (g *Grid[T]) Import() *Grid[T] { g.importOn = true; return g }

// importKinds are the field kinds a CSV cell can fill.
var importKinds = map[FieldKind]bool{
	FieldText: true, FieldTextarea: true, FieldEmail: true, FieldPassword: true,
	FieldURL: true, FieldNumber: true, FieldDecimal: true, FieldCurrency: true,
	FieldHidden: true, FieldSelect: true, FieldRadio: true, FieldSwitch: true,
	FieldColor: true, FieldDate: true, FieldDatetime: true, FieldTime: true,
	FieldMarkdown: true, FieldBelongsTo: true, FieldRichtext: true,
	FieldIcon: true, FieldTags: true,
}

// importOn reports whether the grid imports.
func (t *typedResource[T]) importOn() bool { return t.grid.importOn }

// importFields are the form fields a CSV column can map to.
func (t *typedResource[T]) importFields() []*Field[T] {
	var out []*Field[T]
	for _, fd := range t.form.fields {
		if fd.divider || fd.ignored || fd.virtual || fd.info == nil || fd.embed != nil ||
			fd.onlyUpdate || fd.rangeOf != "" || !importKinds[fd.kind] {
			continue
		}
		out = append(out, fd)
	}
	return out
}

// importTarget is one choice on the mapping page.
type importTarget struct {
	Path  string
	Label string
}

// importTargets lists what a column may map to: the record key first, for
// updates, then each importable field.
func (t *typedResource[T]) importTargets(c *Context) []importTarget {
	out := []importTarget{{Path: t.ft.pk.Path, Label: c.T("%s (updates the record with this key)", t.ft.pk.Label)}}
	for _, fd := range t.importFields() {
		out = append(out, importTarget{Path: fd.path, Label: c.lookup(fd.label)})
	}
	return out
}

// guessTarget matches a CSV header to a field by its label or path, ignoring
// case and spacing, which is what an exported file's headers are.
func guessTarget(header string, targets []importTarget, labels map[string]string) string {
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
			return r == ' ' || r == '_' || r == '-' || r == '.'
		}), ""))
	}
	h := norm(header)
	for _, tg := range targets {
		if h == norm(tg.Path) || h == norm(labels[tg.Path]) {
			return tg.Path
		}
	}
	return ""
}

// readCSV parses a whole upload, tolerating a byte order mark and ragged rows.
func readCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	return r.ReadAll()
}

// ---- pages -------------------------------------------------------------------

type importVM struct {
	Title     string
	BaseURL   string
	Action    string
	MaxSize   string
	Run       *ImportRun
	Columns   []importColumnVM
	Targets   []importTarget
	RowCount  int
	Error     string
	KeyPath   string
	CancelURL string
}

type importColumnVM struct {
	Index    int
	Header   string
	Samples  []string
	Selected string
}

// importAllowed requires a signed-in account as well as Create: a run is
// authorized and reported as the account that queued it.
func (t *typedResource[T]) importAllowed(c *Context) bool {
	return c.User != nil && t.importOn() && t.grid.enabled("create") && t.canCreate(c)
}

// importPage answers GET {slug}/_import: the upload form, or with ?run= the
// mapping page of an uploaded file.
func (t *typedResource[T]) importPage(c *Context) error {
	if !t.importAllowed(c) {
		return t.denyPolicy(c)
	}
	if c.R.URL.Query().Get("run") != "" {
		return t.importMapPage(c)
	}
	return t.renderImport(c, &importVM{}, http.StatusOK)
}

// importPost answers POST {slug}/_import: a file upload, or the confirmed
// mapping of one, which carries its run.
func (t *typedResource[T]) importPost(c *Context) error {
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/form-data") {
		return t.importUpload(c)
	}
	return t.importConfirm(c)
}

// runURL is the mapping page of a run.
func (t *typedResource[T]) runURL(c *Context, run *ImportRun) string {
	return c.URL(t.res.m.slug, "_import") + "?run=" + strconv.FormatUint(uint64(run.ID), 10)
}

func (t *typedResource[T]) renderImport(c *Context, vm *importVM, status int) error {
	m := t.res.m
	vm.Title = m.title
	vm.BaseURL = c.URL(m.slug)
	vm.CancelURL = c.URL(m.slug)
	vm.MaxSize = sizeLabel(importMaxBytes)
	if vm.Action == "" {
		vm.Action = c.URL(m.slug, "_import")
	}
	if status != http.StatusOK {
		c.W.WriteHeader(status)
	}
	return c.Panel.render(c, "grid/import.html", c.T("Import %s", c.lookup(m.title)), vm)
}

// importUpload answers the upload form's POST {slug}/_import: keeps the file and shows the
// mapping page.
func (t *typedResource[T]) importUpload(c *Context) error {
	if !t.importAllowed(c) {
		return t.denyPolicy(c)
	}
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, importMaxBytes+1<<20)
	if err := c.R.ParseMultipartForm(importMaxBytes); err != nil {
		return t.renderImport(c, &importVM{Error: c.T("The file is larger than %s.", sizeLabel(importMaxBytes))}, http.StatusRequestEntityTooLarge)
	}
	file, header, err := c.R.FormFile("file")
	if err != nil {
		return t.renderImport(c, &importVM{Error: c.T("Choose a CSV file to import.")}, http.StatusBadRequest)
	}
	defer func() { _ = file.Close() }()
	if !strings.EqualFold(filepath.Ext(header.Filename), ".csv") {
		return t.renderImport(c, &importVM{Error: c.T("Only CSV files can be imported.")}, http.StatusUnsupportedMediaType)
	}
	data, err := io.ReadAll(io.LimitReader(file, importMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > importMaxBytes {
		return t.renderImport(c, &importVM{Error: c.T("The file is larger than %s.", sizeLabel(importMaxBytes))}, http.StatusRequestEntityTooLarge)
	}
	rows, err := readCSV(data)
	if err != nil || len(rows) < 2 {
		return t.renderImport(c, &importVM{Error: c.T("The file has no rows to import below its header.")}, http.StatusBadRequest)
	}
	run := ImportRun{
		UserID: c.User.ID, TenantID: c.TenantID(), Slug: t.res.m.slug,
		Status: ImportMapping, FileName: safeFileBase(header.Filename) + ".csv", Data: data,
	}
	if err := c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Create(&run).Error; err != nil {
		return err
	}
	return c.Redirect(t.runURL(c, &run))
}

// loadImportRun reads one of the signed-in account's runs for this resource.
func (t *typedResource[T]) loadImportRun(c *Context) (*ImportRun, bool) {
	id, err := strconv.ParseUint(c.R.FormValue("run"), 10, 64)
	if err != nil || c.User == nil {
		return nil, false
	}
	var run ImportRun
	err = c.Panel.db.WithContext(WithoutTenant(c.Ctx())).
		Where("id = ? AND user_id = ? AND slug = ?", uint(id), c.User.ID, t.res.m.slug).
		First(&run).Error
	return &run, err == nil
}

// importMapPage answers GET {slug}/_import?run=: each column with samples
// and the field it fills.
func (t *typedResource[T]) importMapPage(c *Context) error {
	if !t.importAllowed(c) {
		return t.denyPolicy(c)
	}
	run, ok := t.loadImportRun(c)
	if !ok || run.Status != ImportMapping {
		c.Panel.renderError(c, http.StatusNotFound, "Import not found", nil)
		return nil
	}
	rows, err := readCSV(run.Data)
	if err != nil || len(rows) == 0 {
		c.Panel.renderError(c, http.StatusNotFound, "Import not found", nil)
		return nil
	}
	targets := t.importTargets(c)
	labels := map[string]string{t.ft.pk.Path: t.ft.pk.Label}
	for _, fd := range t.importFields() {
		labels[fd.path] = fd.label
	}
	vm := &importVM{
		Run: run, Targets: targets, RowCount: len(rows) - 1, KeyPath: t.ft.pk.Path,
		Action: c.URL(t.res.m.slug, "_import"),
	}
	taken := map[string]bool{}
	for i, h := range rows[0] {
		col := importColumnVM{Index: i, Header: strings.TrimSpace(h)}
		if g := guessTarget(h, targets, labels); g != "" && !taken[g] {
			col.Selected, taken[g] = g, true
		}
		for _, r := range rows[1:min(len(rows), importSampleRows+1)] {
			if i < len(r) {
				col.Samples = append(col.Samples, truncateRunes(r[i], 60))
			}
		}
		vm.Columns = append(vm.Columns, col)
	}
	return t.renderImport(c, vm, http.StatusOK)
}

// importConfirm answers the mapping page's POST {slug}/_import: stores the mapping and
// queues the run.
func (t *typedResource[T]) importConfirm(c *Context) error {
	if !t.importAllowed(c) {
		return t.denyPolicy(c)
	}
	run, ok := t.loadImportRun(c)
	if !ok || run.Status != ImportMapping {
		c.Panel.renderError(c, http.StatusNotFound, "Import not found", nil)
		return nil
	}
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	rows, err := readCSV(run.Data)
	if err != nil || len(rows) == 0 {
		return err
	}
	allowed := map[string]bool{}
	for _, tg := range t.importTargets(c) {
		allowed[tg.Path] = true
	}
	mapping := make([]string, len(rows[0]))
	used := map[string]bool{}
	mapped := false
	for i := range mapping {
		p := c.R.FormValue("col_" + strconv.Itoa(i))
		if p == "" || !allowed[p] || used[p] {
			continue
		}
		mapping[i], used[p] = p, true
		if p != t.ft.pk.Path {
			mapped = true
		}
	}
	if !mapped {
		c.Flash("error", c.T("Match at least one column to a field."))
		return c.Redirect(t.runURL(c, run))
	}
	raw, _ := json.Marshal(mapping)
	err = c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Model(run).
		Updates(map[string]any{"mapping": string(raw), "status": ImportPending}).Error
	if err != nil {
		return err
	}
	if _, err := c.Panel.importJob.Dispatch(c, importPayload{RunID: run.ID}); err != nil {
		return err
	}
	c.Flash("success", c.T("Importing %d rows — you will be notified when it finishes.", len(rows)-1))
	return c.Redirect(c.URL(t.res.m.slug))
}

// ---- the run -------------------------------------------------------------------

type importPayload struct {
	RunID uint
}

// importer is a resource's import behind the type-erased registry.
type importer interface {
	importOn() bool
	runImport(ctx context.Context, run *ImportRun, mapping []string) (*importResult, error)
}

type importResult struct {
	created, updated, failed, total int
	report                          *bytes.Buffer
}

// switchWords are the cell values a Switch column reads as on.
var switchWords = map[string]bool{"1": true, "on": true, "true": true, "yes": true, "y": true, "ya": true}

// runImport feeds every data row through the form's own pipeline, as the
// account that queued the run, and collects the rows that failed into a
// report: the original cells and a last column saying why.
func (t *typedResource[T]) runImport(ctx context.Context, run *ImportRun, mapping []string) (*importResult, error) {
	a := t.res.a
	var user User
	if err := a.db.WithContext(WithoutTenant(ctx)).Preload("Roles").First(&user, run.UserID).Error; err != nil {
		return nil, fmt.Errorf("loading the account that asked: %w", err)
	}
	scoped := WithoutTenant(ctx)
	var state *ctxTenantState
	if a.TenancyEnabled() {
		var err error
		if state, err = a.jobTenantState(ctx, run.TenantID, &user); err != nil {
			return nil, err
		}
		if state.tenant != nil {
			scoped = WithTenant(ctx, state.tenant.ID)
		}
		scoped = context.WithValue(scoped, tenantStateKey{}, state)
	}

	rows, err := readCSV(run.Data)
	if err != nil {
		return nil, fmt.Errorf("reading the file: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("the file is empty")
	}
	res := &importResult{report: &bytes.Buffer{}}
	report := csv.NewWriter(res.report)
	_ = report.Write(append(append([]string{}, rows[0]...), "Error"))

	key := t.ft.pk.Path
	for n, row := range rows[1:] {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		blank := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		res.total++
		form := url.Values{}
		id := ""
		for i, path := range mapping {
			if path == "" || i >= len(row) {
				continue
			}
			cell := strings.TrimSpace(row[i])
			if path == key {
				id = cell
				continue
			}
			if fd := t.form.fieldByPath(path); fd != nil && fd.kind == FieldSwitch {
				form.Set("_present_"+path, "1")
				if switchWords[strings.ToLower(cell)] {
					form.Set(path, "on")
				}
				continue
			}
			form.Set(path, cell)
		}
		msg, updated, err := t.importRow(scoped, &user, form, id)
		if err != nil {
			return res, err
		}
		switch {
		case msg != "":
			res.failed++
			_ = report.Write(append(append([]string{}, row...), a.T("Line %d: %s", n+2, msg)))
		case updated:
			res.updated++
		default:
			res.created++
		}
	}
	report.Flush()
	return res, report.Error()
}

// importRow saves one row through apply, reporting why it was refused.
func (t *typedResource[T]) importRow(ctx context.Context, user *User, form url.Values, id string) (string, bool, error) {
	a := t.res.a
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &Context{R: req, Panel: a, User: user}
	creating := id == ""
	if !creating {
		if _, err := t.repo.Find(ctx, id); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return a.T("No record has the key %s.", id), false, nil
			}
			return "", false, err
		}
	}
	out, err := t.apply(c, id, creating)
	if err != nil {
		// One row's storage failure should not end the run; it is reported
		// with the row and logged in full.
		a.log.Error("steward: import row", "resource", t.res.m.slug, "err", err)
		return a.T("The row could not be saved."), false, nil
	}
	if out.status != 0 {
		return a.lookup(out.message), false, nil
	}
	if len(out.errs) > 0 {
		keys := make([]string, 0, len(out.errs))
		for k := range out.errs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, out.errs[k]...)
		}
		return strings.Join(parts, " "), false, nil
	}
	return "", !creating, nil
}

// runImportJob is the queue handler: it runs the import, stores the report and
// tells the account how it went.
func (a *Panel) runImportJob(ctx context.Context, p importPayload) error {
	db := a.db.WithContext(WithoutTenant(ctx))
	var run ImportRun
	if err := db.First(&run, p.RunID).Error; err != nil {
		return fmt.Errorf("loading import %d: %w", p.RunID, err)
	}
	if run.Status != ImportPending && run.Status != ImportRunning {
		return nil // already finished; a retry has nothing to do
	}
	db.Model(&run).Update("status", ImportRunning)

	fail := func(cause error) error {
		now := time.Now()
		db.Model(&run).Updates(map[string]any{
			"status": ImportFailed, "err": truncateRunes(cause.Error(), 2000), "finished_at": now, "data": nil,
		})
		a.notifyImport(ctx, &run, a.T("Import failed"), cause.Error(), "")
		return nil
	}
	entry, ok := a.bySlug[run.Slug]
	imp, isImporter := entry.(importer)
	if !ok || !isImporter || !imp.importOn() {
		return fail(fmt.Errorf("the %s grid does not import", run.Slug))
	}
	var mapping []string
	if err := json.Unmarshal([]byte(run.Mapping), &mapping); err != nil {
		return fail(fmt.Errorf("reading the column mapping: %w", err))
	}
	res, err := imp.runImport(ctx, &run, mapping)
	if err != nil {
		return fail(err)
	}

	now := time.Now()
	updates := map[string]any{
		"status": ImportDone, "total": res.total, "created": res.created, "updated": res.updated,
		"failed": res.failed, "finished_at": now, "data": nil,
	}
	reportURL := ""
	if res.failed > 0 {
		disk := a.exportDisk()
		name := fmt.Sprintf("imports/%s-%d-errors.csv", run.Slug, run.ID)
		if stored, err := a.storeExport(ctx, disk, name, res.report.Bytes()); err != nil {
			a.log.Error("steward: storing an import report", "run", run.ID, "err", err)
		} else {
			updates["disk"], updates["report_path"] = disk, stored
			reportURL = a.url("_imports", strconv.FormatUint(uint64(run.ID), 10), "report")
		}
	}
	if err := db.Model(&run).Updates(updates).Error; err != nil {
		return err
	}
	name := run.Slug
	if m := entry.meta(); m != nil && m.title != "" {
		name = a.lookup(m.title)
	}
	title := a.T("%s import finished", name)
	if res.failed > 0 {
		title = a.T("%s import finished with errors", name)
	}
	body := a.T("%d created, %d updated, %d failed.", res.created, res.updated, res.failed)
	link := reportURL
	if link == "" {
		link = a.url(run.Slug)
	}
	a.notifyImport(ctx, &run, title, body, link)
	return nil
}

func (a *Panel) notifyImport(ctx context.Context, run *ImportRun, title, body, link string) {
	if !a.notificationsEnabled() {
		return
	}
	n := Notification{Title: title, Body: body, URL: link, Icon: "upload", Type: "import.finished"}
	if err := a.Notify(ctx, run.UserID, n); err != nil {
		a.log.Error("steward: import notification", "run", run.ID, "err", err)
	}
}

// downloadImportReport answers GET {prefix}/_imports/{id}/report: the rows that
// failed, for the account that ran the import.
func (a *Panel) downloadImportReport(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	id, err := strconv.ParseUint(c.R.PathValue("id"), 10, 64)
	if err != nil {
		a.renderError(c, http.StatusNotFound, "Import not found", nil)
		return nil
	}
	var run ImportRun
	if err := a.db.WithContext(WithoutTenant(c.Ctx())).
		Where("id = ? AND user_id = ?", uint(id), c.User.ID).
		First(&run).Error; err != nil || run.ReportPath == "" {
		a.renderError(c, http.StatusNotFound, "Import not found", nil)
		return nil
	}
	d, ok := a.Disk(run.Disk)
	if !ok || d.Storage == nil {
		return fmt.Errorf("no storage for disk %q", run.Disk)
	}
	ls, ok := d.Storage.(*LocalStorage)
	if !ok {
		return c.Redirect(d.Storage.URL(run.ReportPath))
	}
	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+run.Slug+`-import-errors.csv"`)
	c.W.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(c.W, c.R, filepath.Join(ls.Dir, filepath.FromSlash(run.ReportPath)))
	return nil
}

// registerImportJob declares the import job type when any grid imports. It runs
// during build, after resources compile and before the panel is marked built.
func (a *Panel) registerImportJob() {
	for _, entry := range a.registry {
		if imp, ok := entry.(importer); ok && imp.importOn() {
			a.importJob = RegisterJob(a, importJobName, a.runImportJob).Retries(0).Timeout(30 * time.Minute)
			return
		}
	}
}
