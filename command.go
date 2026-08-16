package steward

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// CommandResult is one row the command palette offers.
type CommandResult struct {
	// Group heads the section it appears under — a resource's title, or
	// whatever a CommandSource calls itself.
	Group string `json:"group"`
	// Title is what the reader reads; Subtitle is the dimmer line beside it.
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// URL is where choosing it goes.
	URL string `json:"url"`
	// Icon is a Lucide name, blank for none.
	Icon string `json:"icon,omitempty"`
}

// CommandSource answers the palette for one kind of thing. Register it with
// Admin.CommandSource; it runs on every keystroke past the minimum length, so
// it should be a bounded query rather than a scan.
type CommandSource func(c *Context, query string) []CommandResult

// commandSearcher is the optional half of a resource: one that declares
// QuickSearch can be searched from the palette. Kept off resourceEntry for the
// same reason aggregatorProvider is — the interface describes what every
// resource does, and this is not that.
type commandSearcher interface {
	searchCommand(c *Context, query string, limit int) []CommandResult
}

const (
	// commandMinQuery is the shortest query worth a database round trip. One
	// letter matches most of a table and tells the reader nothing.
	commandMinQuery = 2
	// commandPerSource bounds one section, so no single resource can push the
	// others off the list.
	commandPerSource = 5
)

// commandDeadline caps how long the palette waits. It fires per keystroke, so a
// section that cannot answer inside this is better dropped than shown late.
const commandDeadline = 800 * time.Millisecond

// CommandSource adds a searchable section to the command palette.
//
//	app.CommandSource("Help", func(c *steward.Context, q string) []steward.CommandResult {
//	    return lookupDocs(q)
//	})
//
// Resources that declare QuickSearch are searched already and need no source of
// their own.
func (a *Admin) CommandSource(name string, fn CommandSource) *Admin {
	if a.built {
		panic("steward: CommandSource called after Build")
	}
	a.commandSources = append(a.commandSources, namedCommandSource{name: name, fn: fn})
	return a
}

type namedCommandSource struct {
	name string
	fn   CommandSource
}

// searchCommand implements commandSearcher. A resource that never called
// Command stays out of the palette, as does one this caller may not list.
func (t *typedResource[T]) searchCommand(c *Context, query string, limit int) []CommandResult {
	if len(t.res.commandPaths) == 0 || !t.canViewAny(c) {
		return nil
	}
	q := &ListQuery{PerPage: limit, Page: 1}
	if ids, ok := t.searchIDs(c.Ctx(), query, limit); ok {
		if len(ids) == 0 {
			return nil
		}
		q.Conds = append(q.Conds, Cond{Path: t.ft.pk.Path, Op: OpIn, Val: ids})
		// The palette shows five of them; which five is the whole question.
		q.IDOrder = ids
	} else {
		q.Search = query
		q.SearchPaths = t.res.commandPaths
	}
	// The palette shows a fixed few rows and never pages, so the count that
	// sizes a grid is work nobody reads — and on a large table it is the slow
	// half, scanning every match to reach a number thrown away.
	q.SkipCount = true
	// The same scoping the grid gets, so the palette cannot show a row its
	// own list would hide.
	t.applyRowScope(c, q)
	items, _, err := t.repo.List(c.Ctx(), q)
	if err != nil {
		c.Admin.log.Warn("steward: command search", "resource", t.res.m.slug, "err", err)
		return nil
	}

	out := make([]CommandResult, 0, len(items))
	for i := range items {
		row := &items[i]
		res := CommandResult{
			Group: t.res.m.title,
			// Canonical, because the palette draws it from the sprite by
			// reference rather than through the Go lookup that knows the aliases.
			Icon: canonicalIconName(t.res.m.icon),
			URL:  c.URL(t.res.m.slug, t.rowKey(row)),
		}
		// Per row, not per resource: picking the columns once meant a resource
		// whose first text column is often blank — a subtitle, a nickname —
		// labelled every result "Post #16" and pushed the headline into the
		// dimmer line beside it.
		res.Title, res.Subtitle = t.commandLabels(row)
		if res.Title == "" {
			res.Title = t.res.m.title + " #" + t.rowKey(row)
		}
		out = append(out, res)
	}
	return out
}

// commandLabels reads what a palette row shows: the paths CommandDisplay named,
// or the row's first two non-empty text columns when it named none.
func (t *typedResource[T]) commandLabels(row *T) (title, subtitle string) {
	if len(t.commandCols) > 0 {
		var parts []string
		for _, info := range t.commandCols {
			if s := commandValue(info, row); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", ""
		}
		return parts[0], strings.Join(parts[1:], " · ")
	}
	for _, col := range t.grid.columns {
		if col.hidden || col.computed || col.info == nil || col.info.Kind != kindString {
			continue
		}
		text := t.commandText(col, row)
		if text == "" {
			continue
		}
		if title == "" {
			title = text
			continue
		}
		return title, text
	}
	return title, ""
}

// commandValue reads one path as a single trimmed line, formatting a time the
// way an export does rather than as Go's default rendering of a struct.
func commandValue[T any](info *fieldInfo, row *T) string {
	v, ok := info.value(reflect.ValueOf(row))
	if !ok || v == nil {
		return ""
	}
	var s string
	switch x := v.(type) {
	case time.Time:
		s = x.Format("2006-01-02")
	case *time.Time:
		if x == nil {
			return ""
		}
		s = x.Format("2006-01-02")
	default:
		s = fmt.Sprint(v)
	}
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 90 {
		s = string([]rune(s)[:90]) + "…"
	}
	return s
}

// commandText reads a column's raw value as a single trimmed line.
func (t *typedResource[T]) commandText(col *Column[T], row *T) string {
	if col == nil || col.info == nil {
		return ""
	}
	v, ok := col.info.value(reflect.ValueOf(row))
	if !ok || v == nil {
		return ""
	}
	s := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	if len([]rune(s)) > 90 {
		s = string([]rune(s)[:90]) + "…"
	}
	return s
}

// commandSearch handles GET {prefix}/_command?q=. It answers the palette as the
// reader types, so it stays bounded: a minimum query length, a cap per section,
// and every resource gated by the policy that gates its own list.
func (a *Admin) commandSearch(c *Context) error {
	query := strings.TrimSpace(c.R.URL.Query().Get("q"))
	results := []CommandResult{}
	if len([]rune(query)) < commandMinQuery {
		return c.JSON(http.StatusOK, map[string]any{"results": results})
	}

	// Concurrently, and on a deadline. Run in turn, one table that scans is
	// added to every other table's time; on a deadline, a section that cannot
	// answer in time is left out rather than holding up the ones that can.
	ctx, cancel := context.WithTimeout(c.Ctx(), commandDeadline)
	defer cancel()
	deadlined := c.withContext(ctx)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	collect := func(rs []CommandResult, group string) {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range rs {
			if r.Group == "" {
				r.Group = group
			}
			results = append(results, r)
		}
	}
	for _, entry := range a.registry {
		s, ok := entry.(commandSearcher)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(s commandSearcher) {
			defer wg.Done()
			collect(s.searchCommand(deadlined, query, commandPerSource), "")
		}(s)
	}
	for _, src := range a.commandSources {
		wg.Add(1)
		go func(src namedCommandSource) {
			defer wg.Done()
			collect(src.fn(deadlined, query), src.name)
		}(src)
	}
	wg.Wait()

	// Grouped in a stable order, so the list does not reshuffle between
	// keystrokes for reasons the reader cannot see.
	sort.SliceStable(results, func(i, j int) bool { return results[i].Group < results[j].Group })
	// A section cut off by the deadline contributes nothing, which reads as "no
	// matches" — the same answer a typo gets. Saying which it was is the
	// difference between a reader retyping and a reader concluding the record
	// is not there.
	return c.JSON(http.StatusOK, map[string]any{
		"results": results,
		"partial": ctx.Err() != nil,
	})
}
