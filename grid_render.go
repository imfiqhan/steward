package steward

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
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
	// searchCapped marks a search whose result set is the engine's window
	// rather than every match, so the total is a floor.
	searchCapped bool
	export       string
	ids          []string
	// raw filter input values by param name, echoed back into the panel
	filterVals map[string]string
}

// bareDate matches a day with no time on it.
var bareDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// endOfDay turns a bare upper bound into the exclusive start of the next day,
// which includes the whole of the day named whether the column stores a date or
// a timestamp. It reports false for anything that is not a bare date, or for a
// filter whose values are not dates at all.
func endOfDay(v string, dateLike bool) (string, bool) {
	if !dateLike || !bareDate.MatchString(v) {
		return "", false
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return "", false
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02"), true
}

// filterParam is the query-param name for a filter on the given field path.
// parseState keys filterVals by it and buildVM reads them back out, so the two
// must agree or entered values vanish from the re-rendered filter panel.
func filterParam(path string) string { return "f_" + path }

// parseState turns query params into a ListQuery.
func (t *typedResource[T]) parseState(c *Context) *gridState {
	g := t.grid
	q := c.R.URL.Query()
	st := &gridState{query: &ListQuery{}, filterVals: map[string]string{}}

	st.page, _ = strconv.Atoi(q.Get("page"))
	st.page = max(st.page, 1)
	st.per, _ = strconv.Atoi(q.Get("per_page"))
	if st.per <= 0 || st.per > 500 {
		st.per = g.perPage
	}
	if g.off["pagination"] {
		st.per = 0
	}

	sortChosen := false
	if s := q.Get("sort"); s != "" {
		desc := strings.HasPrefix(s, "-")
		path := strings.TrimPrefix(s, "-")
		if info, ok := t.ft.byPath[path]; ok && info.DBName != "" {
			st.sort = &Sort{Path: path, Desc: desc}
			sortChosen = true
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
		name := filterParam(fi.path)
		v := strings.TrimSpace(q.Get(name))
		v2 := strings.TrimSpace(q.Get(name + "_to"))
		st.filterVals[name] = v
		st.filterVals[name+"_to"] = v2
		if v == "" && v2 == "" {
			continue
		}
		switch fi.op {
		case OpBetween:
			// A bare date as an upper bound means the whole of that day. Left as
			// it is, it compares as that day's midnight, so a range labelled
			// "1–31 July" drops everything written on the 31st after 00:00:00 —
			// 13 rows of 294 on the table this was found against.
			if end, ok := endOfDay(v2, fi.dateLike()); ok {
				if v != "" {
					st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpGte, Val: v})
				}
				st.query.Conds = append(st.query.Conds, Cond{Path: fi.path, Op: OpLt, Val: end})
				continue
			}
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
	st.search = strings.TrimSpace(q.Get("q"))
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
			phrase := strings.Join(bare, " ")
			// An engine, when one is configured and this resource declared what
			// to index. It ranks; the rows are still read through the repository
			// so filters, sorts, and the row scope hold.
			if ids, ok := t.searchIDs(c.Ctx(), phrase, searchWindow); ok {
				// The engine was asked for a window, not for everything, so a
				// full one means "at least this many" — the count that follows
				// counts the window, not the matches.
				st.searchCapped = len(ids) >= searchWindow
				if len(ids) == 0 {
					// Nothing matched. Without this the ID condition is dropped
					// and every row comes back, which reads as "search ignored".
					st.query.Conds = append(st.query.Conds,
						Cond{Path: t.ft.pk.Path, Op: OpIn, Val: []string{}})
				} else {
					st.query.Conds = append(st.query.Conds,
						Cond{Path: t.ft.pk.Path, Op: OpIn, Val: ids})
					// Rank by the engine's order unless the reader picked a
					// column to sort by. Their choice wins; the default sort is
					// not a choice, it is what the page does when nobody said.
					if !sortChosen {
						st.query.IDOrder = ids
					}
				}
			} else {
				st.query.Search = phrase
				st.query.SearchPaths = g.quickSearch
			}
		}
	}

	st.export = q.Get("export")
	if raw := q.Get("ids"); raw != "" {
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
	Depth int
}

type pageLinkVM struct {
	Label    string
	URL      string
	Active   bool
	Disabled bool
}

// perPageOptions folds the active per-page into the selector's options: any
// value can arrive by URL, and the select component blanks its label when
// the current value matches no option.
func perPageOptions(opts []int, per int) []int {
	if per <= 0 || slices.Contains(opts, per) {
		return opts
	}
	merged := append(slices.Clone(opts), per)
	slices.Sort(merged)
	return merged
}

// pageWindow picks the page numbers to render: the first and last page are
// always reachable, with a window around the current page; 0 marks an
// ellipsis. A gap of exactly one page collapses to that page (never "…"
// hiding a single number).
func pageWindow(cur, last int) []int {
	if last <= 7 {
		pages := make([]int, last)
		for i := range pages {
			pages[i] = i + 1
		}
		return pages
	}
	pages := []int{1}
	lo, hi := max(2, cur-1), min(last-1, cur+1)
	switch {
	case lo == 3:
		pages = append(pages, 2)
	case lo > 3:
		pages = append(pages, 0)
	}
	for p := lo; p <= hi; p++ {
		pages = append(pages, p)
	}
	switch {
	case hi == last-2:
		pages = append(pages, last-1)
	case hi < last-2:
		pages = append(pages, 0)
	}
	return append(pages, last)
}

type filterVM struct {
	Param       string
	Label       string
	Input       string // text | select | date | datetime | between | datebetween
	Value       string
	Value2      string
	Placeholder string
	Options     []optionVM
	// Search says the list is too long to ship whole, so the control carries
	// its first page and fetches the rest from OptionsURL as the reader types.
	Search     bool
	OptionsURL string
	// SelectedLabel is what a combobox shows for the value it holds: the label
	// may not be on the page at all once the list is paged.
	SelectedLabel string
}

type optionVM struct {
	Value    string
	Label    string
	Selected bool
}

type gridVM struct {
	Slug, Title string
	BaseURL     string
	Columns     []gridColVM
	Rows        []gridRowVM
	Total       int64
	// TotalCapped marks a total that is the search window rather than the
	// number of matches, so the pager can say so instead of stating a figure
	// that is only a floor.
	TotalCapped    bool
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
	// ActionStyle is "buttons" or "menu"; the template branches on it.
	ActionStyle  string
	RowActions   []actionVM
	BatchActions []actionVM
	ToolActions  []actionVM
	ReorderURL   string
	HeaderTop    []headTopVM // non-empty → two-row grouped header
	HeaderSub    []headSubVM
}

// headTopVM is one first-row header cell: a group label spanning columns,
// or a normal column header with rowspan 2.
type headTopVM struct {
	Group   bool
	Label   string
	Colspan int
	Col     gridColVM
	ColIdx  int
}

type headSubVM struct {
	Col    gridColVM
	ColIdx int
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
		TotalCapped:    st.searchCapped,
		Page:           st.page,
		PerPage:        st.per,
		PerPageOptions: perPageOptions(g.perPageOptions, st.per),
		PerParam:       "per_page",
		SearchParam:    "q",
		Search:         st.search,
		ExportURL:      urlWith(c, map[string]string{"export": "all"}),
		ResetURL:       c.URL(m.slug),
		DeleteURLBase:  c.URL(m.slug),
		ActionStyle:    string(g.actionStyle.resolve(c.Admin.cfg.GridActions)),
		RowActions:     actionVMs(c.URL(m.slug), g.rowActions),
		BatchActions:   actionVMs(c.URL(m.slug), g.batchActions),
		ToolActions:    actionVMs(c.URL(m.slug), g.toolActions),
		ReorderURL:     g.reorderURL,
		Features: map[string]bool{
			"create":      g.enabled("create") && t.canCreate(c),
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
			cv.SortURL = urlWith(c, map[string]string{"sort": next, "page": ""})
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

	// Grouped headers (complex headers).
	activeGroups := make([]headerGroup, 0, len(g.headerGroups))
	for _, hg := range g.headerGroups {
		if hg.span > 0 {
			activeGroups = append(activeGroups, hg)
		}
	}
	if len(activeGroups) > 0 {
		vm.Features["colpick"] = false
		groupAt := map[int]headerGroup{}
		inGroup := map[int]bool{}
		for _, hg := range activeGroups {
			groupAt[hg.start] = hg
			for i := hg.start; i < hg.start+hg.span; i++ {
				inGroup[i] = true
			}
		}
		for i, col := range vm.Columns {
			if hg, ok := groupAt[i]; ok {
				vm.HeaderTop = append(vm.HeaderTop, headTopVM{Group: true, Label: hg.label, Colspan: hg.span})
			}
			if inGroup[i] {
				vm.HeaderSub = append(vm.HeaderSub, headSubVM{Col: col, ColIdx: i})
				continue
			}
			vm.HeaderTop = append(vm.HeaderTop, headTopVM{Col: col, ColIdx: i})
		}
	} else {
		vm.Features["colpick"] = true
	}

	// Filter panel state.
	for _, fi := range g.filters {
		if fi.info == nil {
			continue
		}
		param := filterParam(fi.path)
		fv := filterVM{
			Param:       param,
			Label:       fi.label,
			Value:       st.filterVals[param],
			Value2:      st.filterVals[param+"_to"],
			Placeholder: fi.placeholder,
			Input:       [...]string{"text", "select", "date", "datetime", "between", "datebetween", "daterange"}[fi.input],
		}
		if fv.Value != "" || fv.Value2 != "" {
			vm.ActiveFilters++
		}
		if fv.Input == "select" {
			opts := fi.choices(c)
			for val, label := range opts {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: val == fv.Value})
				if val == fv.Value {
					fv.SelectedLabel = label
				}
			}
			sortOptions(fv.Options)
			// A list that fits one page ships with the grid and is filtered in
			// the browser. A longer one would otherwise put every option in the
			// HTML of every page of the grid.
			fv.Search = len(fv.Options) > optionSearchLimit
			if fv.Search {
				fv.Options = firstPage(fv.Options)
				fv.OptionsURL = filterOptionsURL(c, m.slug, param)
			}
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
			return urlWith(c, map[string]string{"page": strconv.Itoa(p)})
		}
		vm.Pagination = append(vm.Pagination, pageLinkVM{Label: "prev", URL: pageURL(st.page - 1), Disabled: st.page <= 1})
		for _, p := range pageWindow(st.page, vm.Pages) {
			if p == 0 {
				vm.Pagination = append(vm.Pagination, pageLinkVM{Label: "gap", Disabled: true})
				continue
			}
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
	raw := val
	for _, tr := range col.transform {
		val = tr(val, row)
	}
	// A badge's colour is keyed on the stored value, but Using has already
	// replaced it with display text by now. Carrying both keeps the colour
	// while the text is what Using asked for.
	if col.badges != nil && len(col.transform) > 0 {
		val = labeledValue{key: raw, text: fmt.Sprint(val)}
	}
	// After the transforms, so Using can pick the path and this resolves it —
	// and before the presenter, which is what needs a fetchable reference.
	if col.storageRef && val != nil {
		s := fmt.Sprint(val)
		val = resolvedRef{raw: s, url: t.res.a.DiskURL(col.disk, s)}
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
			`<input type="checkbox" role="switch" class="input" %s data-steward-inline-switch data-url="%s" data-field="%s" aria-label="Toggle %s"/>`,
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
		return `<span class="text-muted-foreground">—</span>`
	case time.Time:
		if x.IsZero() {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(x.Format("2006-01-02 15:04")))
	case *time.Time:
		if x == nil || x.IsZero() {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(x.Format("2006-01-02 15:04")))
	case bool:
		return statusHTML(x, "Yes", "No")
	default:
		s := fmt.Sprint(v)
		if s == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(template.HTMLEscapeString(s))
	}
}

// ---- handlers ---------------------------------------------------------------

func (t *typedResource[T]) index(c *Context) error {
	if !t.canViewAny(c) {
		return t.denyPolicy(c)
	}
	st := t.parseState(c)
	t.applyRowScope(c, st.query) // rides into list, tree, and export queries

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

	// Tree mode: whole hierarchy in depth-first order, unless the user is
	// searching/filtering (flat results make more sense there).
	if t.grid.treePath != "" && st.search == "" && len(st.query.Conds) == 0 {
		items, depths, total, err := t.loadTree(c, st)
		if err != nil {
			return err
		}
		vm := t.buildVM(c, st, items, total)
		t.decorateTree(vm, depths)
		return c.Admin.render(c, "grid/index.html", t.res.m.title, vm)
	}

	items, total, err := t.repo.List(c.Ctx(), st.query)
	if err != nil {
		return err
	}
	vm := t.buildVM(c, st, items, total)
	return c.Admin.render(c, "grid/index.html", t.res.m.title, vm)
}

// loadTree fetches every row and orders it depth-first under the parent key.
func (t *typedResource[T]) loadTree(c *Context, st *gridState) ([]T, []int, int64, error) {
	q := *st.query
	q.Page, q.PerPage = 1, 1000
	items, total, err := t.repo.List(c.Ctx(), &q)
	if err != nil {
		return nil, nil, 0, err
	}
	parentInfo, ok := t.ft.byPath[t.grid.treePath]
	if !ok {
		return items, nil, total, nil
	}

	children := map[string][]int{} // parent key → item indexes, keeps sort order
	for i := range items {
		pv, _ := parentInfo.value(reflect.ValueOf(&items[i]))
		parent := fmt.Sprint(pv)
		if pv == nil {
			parent = "0"
		}
		children[parent] = append(children[parent], i)
	}

	var orderedIdx []int
	var depths []int
	seen := make(map[int]bool, len(items))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, idx := range children[parent] {
			if seen[idx] {
				continue // cycles never recurse forever
			}
			seen[idx] = true
			orderedIdx = append(orderedIdx, idx)
			depths = append(depths, depth)
			walk(t.rowKey(&items[idx]), depth+1)
		}
	}
	for _, root := range []string{"0", "", "<nil>"} {
		walk(root, 0)
	}
	// Orphans (parent not in the result set) append at root level.
	for i := range items {
		if !seen[i] {
			orderedIdx = append(orderedIdx, i)
			depths = append(depths, 0)
		}
	}

	ordered := make([]T, len(orderedIdx))
	for pos, idx := range orderedIdx {
		ordered[pos] = items[idx]
	}
	return ordered, depths, total, nil
}

// decorateTree injects depth attributes and carets into the built rows.
func (t *typedResource[T]) decorateTree(vm *gridVM, depths []int) {
	if len(depths) != len(vm.Rows) {
		return
	}
	vm.Features["pagination"] = false
	firstCol := 0
	for i, col := range vm.Columns {
		if !col.Hidden {
			firstCol = i
			break
		}
	}
	for i := range vm.Rows {
		vm.Rows[i].Depth = depths[i]
		hasChildren := i+1 < len(depths) && depths[i+1] > depths[i]
		if len(vm.Rows[i].Cells) <= firstCol {
			continue
		}
		prefix := fmt.Sprintf(`<span class="steward-tree-pad" style="display:inline-block;width:%dpx"></span>`, depths[i]*24)
		if hasChildren {
			prefix += `<button type="button" class="btn steward-tree-toggle" data-variant="ghost" data-size="icon-sm" data-steward-tree-toggle aria-label="Collapse children" aria-expanded="true">▾</button> `
		}
		vm.Rows[i].Cells[firstCol] = template.HTML(prefix) + vm.Rows[i].Cells[firstCol]
	}
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
	if t.policy != nil {
		for _, id := range ids {
			m, err := t.repo.Find(c.Ctx(), id)
			if err != nil {
				continue // vanished row; the delete below no-ops it
			}
			if !t.policy.Delete(c, m) {
				return c.Envelope(Error("You do not have permission to delete this.").Code(http.StatusForbidden))
			}
		}
	}
	if t.form.deletingFn != nil {
		if err := t.form.deletingFn(c, ids); err != nil {
			return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
		}
	}
	if err := t.repo.Delete(c.Ctx(), ids); err != nil {
		return err
	}
	t.unindexRows(c.Ctx(), ids)
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
