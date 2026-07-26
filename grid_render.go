package steward

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/imfiqhan/steward/internal/quickdsl"
)

// gridState is the parsed request state for one grid load.
type gridState struct {
	query  *ListQuery
	page   int
	per    int
	sort   *Sort
	search string
	export string
	ids    []string
	// raw filter input values by param name, echoed back into the panel
	filterVals map[string]string
}

func (t *typedResource[T]) param(name string) string { return t.res.m.slug + "_" + name }

// parseState turns namespaced query params into a ListQuery.
func (t *typedResource[T]) parseState(c *Context) *gridState {
	g := t.grid
	q := c.R.URL.Query()
	st := &gridState{query: &ListQuery{}, filterVals: map[string]string{}}

	st.page, _ = strconv.Atoi(q.Get(t.param("page")))
	st.page = max(st.page, 1)
	st.per, _ = strconv.Atoi(q.Get(t.param("per")))
	if st.per <= 0 || st.per > 500 {
		st.per = g.perPage
	}
	if g.off["pagination"] {
		st.per = 0
	}

	if s := q.Get(t.param("sort")); s != "" {
		desc := strings.HasPrefix(s, "-")
		path := strings.TrimPrefix(s, "-")
		if info, ok := t.ft.byPath[path]; ok && info.DBName != "" {
			st.sort = &Sort{Path: path, Desc: desc}
		}
	}
	if st.sort == nil {
		st.sort = g.defaultSort
	}
	if st.sort == nil {
		st.sort = &Sort{Path: t.ft.pk.Path, Desc: true}
	}
	st.query.Sorts = []Sort{*st.sort}
	st.query.Page, st.query.PerPage = st.page, st.per

	// Filters.
	for _, fi := range g.filters {
		if fi.info == nil {
			continue
		}
		name := t.param("f_" + fi.path)
		v := strings.TrimSpace(q.Get(name))
		v2 := strings.TrimSpace(q.Get(name + "_to"))
		st.filterVals[name] = v
		st.filterVals[name+"_to"] = v2
		if v == "" && v2 == "" {
			continue
		}
		switch fi.op {
		case OpBetween:
			switch {
			case v != "" && v2 != "":
				st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpBetween, Val: v, Val2: v2})
			case v != "":
				st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpGte, Val: v})
			default:
				st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpLte, Val: v2})
			}
		case OpIn:
			st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpIn, Val: strings.Split(v, ",")})
		default:
			st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: fi.op, Val: v})
		}
	}

	// Quick search through the mini-DSL.
	st.search = strings.TrimSpace(q.Get(t.param("q")))
	if st.search != "" && len(g.quickSearch) > 0 && g.enabled("quicksearch") {
		var bare []string
		for _, term := range quickdsl.Parse(st.search) {
			if term.Field == "" {
				bare = append(bare, term.Values[0])
				continue
			}
			path, ok := t.resolveSearchField(term.Field)
			if !ok {
				bare = append(bare, term.Field+":"+strings.Join(term.Values, ","))
				continue
			}
			st.query.SearchConds = append(st.query.SearchConds, dslCond(path, term))
		}
		if len(bare) > 0 {
			st.query.Search = strings.Join(bare, " ")
			st.query.SearchPaths = g.quickSearch
		}
	}

	st.export = q.Get(t.param("export"))
	if raw := q.Get(t.param("ids")); raw != "" {
		st.ids = strings.Split(raw, ",")
	}
	return st
}

// resolveSearchField matches a DSL field name against model paths,
// case-insensitively.
func (t *typedResource[T]) resolveSearchField(name string) (string, bool) {
	if _, ok := t.ft.byPath[name]; ok {
		return name, true
	}
	lower := strings.ToLower(name)
	for p := range t.ft.byPath {
		if strings.ToLower(p) == lower {
			return p, true
		}
	}
	return "", false
}

func dslCond(path string, term quickdsl.Term) Cond {
	v := ""
	if len(term.Values) > 0 {
		v = term.Values[0]
	}
	switch term.Op {
	case quickdsl.OpEq:
		return Cond{Path: path, Op: OpEq, Val: v}
	case quickdsl.OpNe:
		return Cond{Path: path, Op: OpNe, Val: v}
	case quickdsl.OpGt:
		return Cond{Path: path, Op: OpGt, Val: v}
	case quickdsl.OpGte:
		return Cond{Path: path, Op: OpGte, Val: v}
	case quickdsl.OpLt:
		return Cond{Path: path, Op: OpLt, Val: v}
	case quickdsl.OpLte:
		return Cond{Path: path, Op: OpLte, Val: v}
	case quickdsl.OpLike:
		return Cond{Path: path, Op: OpLike, Val: v}
	case quickdsl.OpPrefix:
		return Cond{Path: path, Op: OpPrefix, Val: v}
	case quickdsl.OpIn:
		return Cond{Path: path, Op: OpIn, Val: term.Values}
	case quickdsl.OpBetween:
		return Cond{Path: path, Op: OpBetween, Val: term.Values[0], Val2: term.Values[1]}
	case quickdsl.OpNull:
		return Cond{Path: path, Op: OpNull, Val: true}
	default:
		return Cond{Path: path, Op: OpEq, Val: v}
	}
}

// ---- view model -----------------------------------------------------------

type gridColVM struct {
	Label     string
	Help      string
	Sortable  bool
	SortURL   string
	SortState string // "", "asc", "desc"
	Hidden    bool
	Width     int
}

type gridRowVM struct {
	Key   string
	Cells []template.HTML
}

type pageLinkVM struct {
	Label    string
	URL      string
	Active   bool
	Disabled bool
}

type filterVM struct {
	Param       string
	Label       string
	Input       string // text | select | date | datetime | between | datebetween
	Value       string
	Value2      string
	Placeholder string
	Options     []optionVM
}

type optionVM struct {
	Value    string
	Label    string
	Selected bool
}

type gridVM struct {
	Slug, Title    string
	BaseURL        string
	Columns        []gridColVM
	Rows           []gridRowVM
	Total          int64
	Page, Pages    int
	From, To       int
	PerPage        int
	PerPageOptions []int
	PerParam       string
	SearchParam    string
	Search         string
	Filters        []filterVM
	ActiveFilters  int
	Pagination     []pageLinkVM
	ExportURL      string
	ResetURL       string
	Features       map[string]bool
	DeleteURLBase  string
	RowActions     []actionVM
	BatchActions   []actionVM
	ToolActions    []actionVM
	ReorderURL     string
}

// urlWith rebuilds the current URL with parameter overrides ("" deletes).
func urlWith(c *Context, overrides map[string]string) string {
	q := c.R.URL.Query()
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	u := url.URL{Path: c.R.URL.Path, RawQuery: q.Encode()}
	return u.String()
}

func (t *typedResource[T]) buildVM(c *Context, st *gridState, items []T, total int64) *gridVM {
	g := t.grid
	m := t.res.m
	vm := &gridVM{
		Slug:           m.slug,
		Title:          m.title,
		BaseURL:        c.URL(m.slug),
		Total:          total,
		Page:           st.page,
		PerPage:        st.per,
		PerPageOptions: g.perPageOptions,
		PerParam:       t.param("per"),
		SearchParam:    t.param("q"),
		Search:         st.search,
		ExportURL:      urlWith(c, map[string]string{t.param("export"): "all"}),
		ResetURL:       c.URL(m.slug),
		DeleteURLBase:  c.URL(m.slug),
		RowActions:     actionVMs(c.URL(m.slug), g.rowActions),
		BatchActions:   actionVMs(c.URL(m.slug), g.batchActions),
		ToolActions:    actionVMs(c.URL(m.slug), g.toolActions),
		ReorderURL:     g.reorderURL,
		Features: map[string]bool{
			"create":      g.enabled("create"),
			"delete":      g.enabled("delete"),
			"edit":        g.enabled("edit"),
			"view":        g.enabled("view"),
			"filter":      g.enabled("filter") && len(g.filters) > 0,
			"export":      g.enabled("export"),
			"selector":    g.enabled("selector") && (g.enabled("delete") || len(g.batchActions) > 0),
			"pagination":  g.enabled("pagination"),
			"quicksearch": g.enabled("quicksearch") && len(g.quickSearch) > 0,
			"actions":     g.enabled("delete") || g.enabled("edit") || g.enabled("view") || len(g.rowActions) > 0,
		},
	}

	for _, col := range g.columns {
		cv := gridColVM{Label: col.label, Help: col.help, Hidden: col.hidden, Width: col.width, Sortable: col.sortable && !col.computed}
		if cv.Sortable {
			next := col.path
			if st.sort != nil && st.sort.Path == col.path {
				if st.sort.Desc {
					cv.SortState = "desc"
				} else {
					cv.SortState = "asc"
					next = "-" + col.path
				}
			}
			cv.SortURL = urlWith(c, map[string]string{t.param("sort"): next, t.param("page"): ""})
		}
		vm.Columns = append(vm.Columns, cv)
	}

	for i := range items {
		row := &items[i]
		rv := gridRowVM{Key: t.rowKey(row)}
		for _, col := range g.columns {
			rv.Cells = append(rv.Cells, t.renderCell(col, row))
		}
		vm.Rows = append(vm.Rows, rv)
	}

	// Filter panel state.
	for _, fi := range g.filters {
		if fi.info == nil {
			continue
		}
		param := t.param("f_" + fi.path)
		fv := filterVM{
			Param:       param,
			Label:       fi.label,
			Value:       st.filterVals[param],
			Value2:      st.filterVals[param+"_to"],
			Placeholder: fi.placeholder,
			Input:       [...]string{"text", "select", "date", "datetime", "between", "datebetween"}[fi.input],
		}
		if fv.Value != "" || fv.Value2 != "" {
			vm.ActiveFilters++
		}
		for val, label := range fi.options {
			fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: val == fv.Value})
		}
		vm.Filters = append(vm.Filters, fv)
	}

	// Pagination.
	if st.per > 0 {
		vm.Pages = int((total + int64(st.per) - 1) / int64(st.per))
		if len(vm.Rows) > 0 {
			vm.From = (st.page-1)*st.per + 1
			vm.To = vm.From + len(vm.Rows) - 1
		}
		pageURL := func(p int) string {
			return urlWith(c, map[string]string{t.param("page"): strconv.Itoa(p)})
		}
		vm.Pagination = append(vm.Pagination, pageLinkVM{Label: "prev", URL: pageURL(st.page - 1), Disabled: st.page <= 1})
		lo, hi := max(1, st.page-3), min(vm.Pages, st.page+3)
		for p := lo; p <= hi; p++ {
			vm.Pagination = append(vm.Pagination, pageLinkVM{Label: strconv.Itoa(p), URL: pageURL(p), Active: p == st.page})
		}
		vm.Pagination = append(vm.Pagination, pageLinkVM{Label: "next", URL: pageURL(st.page + 1), Disabled: st.page >= vm.Pages})
	}
	return vm
}

func (t *typedResource[T]) rowKey(row *T) string {
	v, ok := t.ft.pk.value(reflect.ValueOf(row))
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

// renderCell resolves the value, applies transforms, and presents it.
func (t *typedResource[T]) renderCell(col *Column[T], row *T) template.HTML {
	var val any
	if !col.computed {
		if col.info == nil {
			return "—"
		}
		v, ok := col.info.value(reflect.ValueOf(row))
		if !ok {
			val = nil
		} else {
			val = v
		}
	}
	if col.inline != inlineNone && !col.computed {
		return t.renderInlineCell(col, row, val)
	}
	for _, tr := range col.transform {
		val = tr(val, row)
	}
	if col.present != nil {
		return col.present(val, row)
	}
	return defaultCell(val)
}

// renderInlineCell emits live-edit controls that PUT the single field to the
// standard update route (validated by the matching form field).
func (t *typedResource[T]) renderInlineCell(col *Column[T], row *T, val any) template.HTML {
	url := template.HTMLEscapeString("/" + strings.TrimLeft(t.res.a.url(t.res.m.slug, t.rowKey(row)), "/"))
	field := template.HTMLEscapeString(col.path)
	switch col.inline {
	case inlineSwitch:
		checked := ""
		if truthy(val) {
			checked = "checked"
		}
		return template.HTML(fmt.Sprintf(
			`<label class="form-check form-switch m-0"><input type="checkbox" class="form-check-input" %s data-steward-inline-switch data-url="%s" data-field="%s" aria-label="Toggle %s"/></label>`,
			checked, url, field, field))
	case inlineText:
		s := ""
		if val != nil {
			s = fmt.Sprint(val)
		}
		esc := template.HTMLEscapeString(s)
		return template.HTML(fmt.Sprintf(
			`<span class="steward-editable" tabindex="0" role="button" data-steward-editable data-url="%s" data-field="%s" title="Click to edit">%s</span>`,
			url, field, esc))
	default:
		return defaultCell(val)
	}
}

// defaultCell formats a raw value for display.
func defaultCell(v any) template.HTML {
	switch x := v.(type) {
	case nil:
		return `<span class="text-secondary">—</span>`
	case time.Time:
		if x.IsZero() {
			return `<span class="text-secondary">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(x.Format("2006-01-02 15:04")))
	case *time.Time:
		if x == nil || x.IsZero() {
			return `<span class="text-secondary">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(x.Format("2006-01-02 15:04")))
	case bool:
		if x {
			return `<span class="status status-green">Yes</span>`
		}
		return `<span class="status status-secondary">No</span>`
	default:
		s := fmt.Sprint(v)
		if s == "" {
			return `<span class="text-secondary">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(s))
	}
}

// ---- handlers ---------------------------------------------------------------

func (t *typedResource[T]) index(c *Context) error {
	st := t.parseState(c)

	if st.export != "" && t.grid.enabled("export") {
		return t.exportCSV(c, st)
	}

	if c.WantsJSON() {
		items, total, err := t.repo.List(c.Ctx(), st.query)
		if err != nil {
			return err
		}
		c.W.Header().Set("Vary", "HX-Request, Accept")
		return c.JSON(http.StatusOK, map[string]any{
			"items":    items,
			"total":    total,
			"page":     st.page,
			"per_page": st.per,
		})
	}

	items, total, err := t.repo.List(c.Ctx(), st.query)
	if err != nil {
		return err
	}
	vm := t.buildVM(c, st, items, total)
	return c.Admin.render(c, "grid/index.html", t.res.m.title, vm)
}

func (t *typedResource[T]) destroy(c *Context) error {
	if !t.grid.enabled("delete") {
		return c.Envelope(Error("Deleting is disabled for this resource.").Code(http.StatusForbidden))
	}
	raw := c.R.PathValue("id")
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return c.Envelope(Error("Nothing selected.").Code(http.StatusBadRequest))
	}
	if t.form.deletingFn != nil {
		if err := t.form.deletingFn(c, ids); err != nil {
			return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
		}
	}
	if err := t.repo.Delete(c.Ctx(), ids); err != nil {
		return err
	}
	if t.form.deletedFn != nil {
		if err := t.form.deletedFn(c, ids); err != nil {
			c.Admin.log.Error("steward: deleted hook", "err", err)
		}
	}
	noun := "record"
	if len(ids) > 1 {
		noun = fmt.Sprintf("%d records", len(ids))
	}
	return c.Envelope(Success("Deleted " + noun + ".").Refresh())
}

// exportCSV streams the current view (all pages, current page, or a
// selection) honoring active filters, search, and sort.
func (t *typedResource[T]) exportCSV(c *Context, st *gridState) error {
	q := *st.query
	switch st.export {
	case "page":
		// keep pagination as-is
	case "selected":
		if len(st.ids) == 0 {
			return c.Envelope(Error("Nothing selected.").Code(http.StatusBadRequest))
		}
		q.Conds = append(q.Conds, Cond{Path: t.ft.pk.Path, Op: OpIn, Val: st.ids})
		q.Page, q.PerPage = 0, 0
	default: // "all"
		q.Page, q.PerPage = 0, 0
	}

	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+t.res.m.slug+`.csv"`)
	w := csv.NewWriter(c.W)

	var header []string
	for _, col := range t.grid.columns {
		header = append(header, col.label)
	}
	if err := w.Write(header); err != nil {
		return err
	}

	const batch = 1000
	page := 1
	for {
		bq := q
		if bq.PerPage == 0 {
			bq.Page, bq.PerPage = page, batch
		}
		items, _, err := t.repo.List(c.Ctx(), &bq)
		if err != nil {
			return err
		}
		for i := range items {
			row := &items[i]
			rec := make([]string, 0, len(t.grid.columns))
			for _, col := range t.grid.columns {
				rec = append(rec, t.rawCell(col, row))
			}
			if err := w.Write(rec); err != nil {
				return err
			}
		}
		if q.PerPage != 0 || len(items) < batch {
			break
		}
		page++
	}
	w.Flush()
	return w.Error()
}

// rawCell is the CSV/text form of a cell: transforms applied, no HTML.
func (t *typedResource[T]) rawCell(col *Column[T], row *T) string {
	var val any
	if !col.computed {
		if col.info == nil {
			return ""
		}
		v, ok := col.info.value(reflect.ValueOf(row))
		if ok {
			val = v
		}
	}
	for _, tr := range col.transform {
		val = tr(val, row)
	}
	switch x := val.(type) {
	case nil:
		return ""
	case time.Time:
		return x.Format(time.RFC3339)
	case *time.Time:
		if x == nil {
			return ""
		}
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprint(val)
	}
}
