package steward

import (
	"net/http"
	"strings"
)

// TrashPolicy is the optional half of a Policy that decides restore and
// permanent delete separately. Without it both are allowed to whoever the
// policy's Delete allows.
type TrashPolicy[T any] interface {
	Restore(c *Context, m *T) bool
	ForceDelete(c *Context, m *T) bool
}

// DisableTrash hides the trash view of a resource whose model is soft-deleted.
// Deletes stay soft; there is just no way back from the panel.
func (g *Grid[T]) DisableTrash() *Grid[T] { g.off["trash"] = true; return g }

// compileTrash turns the trash on for a soft-deleting repository whose grid
// deletes.
func (t *typedResource[T]) compileTrash() {
	t.trash = nil
	tr, ok := t.repo.(Trasher[T])
	if !ok || !tr.Trashable() || !t.grid.enabled("trash") || !t.grid.enabled("delete") {
		return
	}
	t.trash = tr
}

func (t *typedResource[T]) canRestore(c *Context, m *T) bool {
	if tp, ok := t.policy.(TrashPolicy[T]); ok {
		return tp.Restore(c, m)
	}
	return t.canDelete(c, m)
}

func (t *typedResource[T]) canForceDelete(c *Context, m *T) bool {
	if tp, ok := t.policy.(TrashPolicy[T]); ok {
		return tp.ForceDelete(c, m)
	}
	return t.canDelete(c, m)
}

// trashedRows loads the deleted rows among ids, through the row scope.
func (t *typedResource[T]) trashedRows(c *Context, ids []string) ([]T, error) {
	q := &ListQuery{Conds: []Cond{{Path: t.ft.pk.Path, Op: OpIn, Val: ids}}}
	t.applyRowScope(c, q)
	rows, _, err := t.trash.ListTrashed(c.Ctx(), q)
	return rows, err
}

// formIDs reads the comma-separated ids a grid action posts.
func formIDs(c *Context) ([]string, error) {
	if err := c.R.ParseForm(); err != nil {
		return nil, err
	}
	var ids []string
	for _, raw := range c.R.Form["ids"] {
		for id := range strings.SplitSeq(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// trashRequest is the shared front of restore and permanent delete: the
// trashed rows among the posted ids that the reader may act on. When it
// answers the request itself — nothing selected, a denial — handled is true and
// err is the result of writing that answer. A denial of any row refuses the
// whole request, so a batch is never half applied.
func (t *typedResource[T]) trashRequest(c *Context, allowed func(*Context, *T) bool) (rows []T, handled bool, err error) {
	if t.trash == nil {
		return nil, true, c.Envelope(Error("There is no trash for this resource.").Code(http.StatusNotFound))
	}
	if !t.canViewAny(c) {
		return nil, true, t.denyPolicy(c)
	}
	ids, err := formIDs(c)
	if err != nil {
		return nil, true, err
	}
	if len(ids) == 0 {
		return nil, true, c.Envelope(Error("Nothing selected.").Code(http.StatusBadRequest))
	}
	rows, err = t.trashedRows(c, ids)
	if err != nil {
		return nil, true, err
	}
	for i := range rows {
		if !allowed(c, &rows[i]) {
			return nil, true, c.Envelope(Error("You do not have permission to do this.").Code(http.StatusForbidden))
		}
	}
	return rows, false, nil
}

// restoreRows answers POST {slug}/_restore.
func (t *typedResource[T]) restoreRows(c *Context) error {
	rows, handled, err := t.trashRequest(c, t.canRestore)
	if handled {
		return err
	}
	keys := t.rowKeys(rows)
	if err := t.trash.Restore(c.Ctx(), keys); err != nil {
		return err
	}
	for i := range rows {
		t.indexRow(c.Ctx(), &rows[i])
	}
	msg := c.T("Restored record.")
	if len(keys) != 1 {
		msg = c.T("Restored %d records.", len(keys))
	}
	return c.Envelope(Success(msg).Refresh())
}

// forceDeleteRows answers POST {slug}/_force_delete. The Deleting and Deleted
// hooks run, since the rows are gone for good.
func (t *typedResource[T]) forceDeleteRows(c *Context) error {
	rows, handled, err := t.trashRequest(c, t.canForceDelete)
	if handled {
		return err
	}
	keys := t.rowKeys(rows)
	if t.form.deletingFn != nil {
		if err := t.form.deletingFn(c, keys); err != nil {
			return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
		}
	}
	if err := t.trash.ForceDelete(c.Ctx(), keys); err != nil {
		return err
	}
	if t.form.deletedFn != nil {
		if err := t.form.deletedFn(c, keys); err != nil {
			c.Panel.log.Error("steward: deleted hook", "err", err)
		}
	}
	msg := c.T("Deleted record permanently.")
	if len(keys) != 1 {
		msg = c.T("Deleted %d records permanently.", len(keys))
	}
	return c.Envelope(Success(msg).Refresh())
}

func (t *typedResource[T]) rowKeys(rows []T) []string {
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, t.rowKey(&rows[i]))
	}
	return out
}

// trashVM is the toolbar's way into and out of the trash.
type trashVM struct {
	Active bool
	URL    string
	Count  int64
}

// trashActions are the row and batch actions of the trash view.
func trashActions(c *Context, slug string) []actionVM {
	return []actionVM{
		{Name: "restore", Label: "Restore", Icon: "rotate-ccw", URL: c.URL(slug, "_restore")},
		{Name: "force-delete", Label: "Delete permanently", Icon: "trash-2", Danger: true,
			Confirm: c.T("Delete permanently? This cannot be undone."), URL: c.URL(slug, "_force_delete")},
	}
}
