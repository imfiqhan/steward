package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// RecordChange is one entry of a record's history: who did what to it, and for
// a create or an update, each column's value before and after.
type RecordChange struct {
	ID        uint   `gorm:"primaryKey"`
	Resource  string `gorm:"size:120;not null;index:idx_record_history_lookup,priority:1"`
	RecordKey string `gorm:"size:64;not null;index:idx_record_history_lookup,priority:2"`
	// Action is one of the Change constants.
	Action   string `gorm:"size:16;not null"`
	UserID   uint   `gorm:"default:0"`
	TenantID uint   `gorm:"index;default:0"`
	// Changes is a JSON object of field path to [before, after]. Empty for a
	// delete, restore or permanent delete.
	Changes   string `gorm:"type:text"`
	CreatedAt time.Time
}

func (RecordChange) TableName() string { return prefixed("record_history") }

// Record history actions.
const (
	ChangeCreated      = "created"
	ChangeUpdated      = "updated"
	ChangeDeleted      = "deleted"
	ChangeRestored     = "restored"
	ChangeForceDeleted = "force_deleted"
)

// historyValueLimit bounds one stored value, in runes. History is for reading
// what changed, not for keeping a second copy of every article body.
const historyValueLimit = 1000

// History records every change made to this resource's records through the
// panel — creates, edits including inline ones, deletes, restores — with the
// account that made it and each changed field's value before and after. The
// detail page shows the latest entries.
//
// Writes that bypass the form are not seen: a custom action or hook writing
// through GORM directly, and the rows of a HasMany repeater.
func (r *Resource[T]) History() *Resource[T] { r.history = true; return r }

// Change is one field of a history entry, as History returns it.
type Change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// FieldChanges decodes the entry's field changes.
func (rc *RecordChange) FieldChanges() map[string]Change {
	out := map[string]Change{}
	if rc.Changes == "" {
		return out
	}
	var raw map[string][2]any
	if err := json.Unmarshal([]byte(rc.Changes), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		out[k] = Change{Before: v[0], After: v[1]}
	}
	return out
}

// History returns a record's latest history entries, newest first, in the
// tenant ctx carries.
func (a *Panel) History(ctx context.Context, slug, key string, limit int) ([]RecordChange, error) {
	if limit <= 0 {
		limit = 20
	}
	db := a.db.WithContext(WithoutTenant(ctx)).
		Where("resource = ? AND record_key = ?", slug, key)
	if id, ok := TenantFromContext(ctx); ok {
		db = db.Where("tenant_id = ?", id)
	}
	var out []RecordChange
	err := db.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// PruneHistory deletes history entries older than age. Nothing calls it for
// you.
func (a *Panel) PruneHistory(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, errors.New("steward: PruneHistory needs a positive age")
	}
	res := a.db.WithContext(WithoutTenant(ctx)).
		Where("created_at < ?", time.Now().Add(-age)).Delete(&RecordChange{})
	return res.RowsAffected, res.Error
}

// historyOn reports whether this resource records history.
func (t *typedResource[T]) historyOn() bool { return t.res.history }

// masked reports whether a path's values must not be stored in clear: a
// password field, or a column the operation log masks too.
func (t *typedResource[T]) masked(path string) bool {
	if fd := t.form.fieldByPath(path); fd != nil && fd.kind == FieldPassword {
		return true
	}
	info := t.ft.byPath[path]
	name := strings.ToLower(path)
	if info != nil {
		name = strings.ToLower(info.DBName)
	}
	return slices.ContainsFunc(maskedFields, func(m string) bool { return strings.Contains(name, m) })
}

// historyValue turns a column value into what an entry stores: plain JSON
// values as they are, times as text, anything else through fmt, and long
// text cut short.
func historyValue(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	v = rv.Interface()
	switch x := v.(type) {
	case time.Time:
		if x.IsZero() {
			return nil
		}
		return x.Format("2006-01-02 15:04:05")
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return x
	case string:
		return truncateRunes(x, historyValueLimit)
	}
	switch rv.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Map:
		if raw, err := json.Marshal(v); err == nil {
			return truncateRunes(string(raw), historyValueLimit)
		}
	}
	return truncateRunes(fmt.Sprint(v), historyValueLimit)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// fieldChanges builds an entry's changes for the given paths, from a snapshot
// taken before the write (nil on a create) and the record after it.
func (t *typedResource[T]) fieldChanges(paths []string, before map[string]any, m *T) map[string][2]any {
	out := map[string][2]any{}
	rv := reflect.ValueOf(m)
	for _, p := range paths {
		info := t.ft.byPath[p]
		if info == nil || info.Relation != "" || info.DBName == "" || info.Primary {
			continue
		}
		var after any
		if v, ok := info.value(rv); ok {
			after = v
		}
		var prev any
		if before != nil {
			prev = before[p]
		}
		if t.masked(p) {
			out[p] = [2]any{maskValue(fmt.Sprint(prev)), maskValue(fmt.Sprint(after))}
			continue
		}
		out[p] = [2]any{historyValue(prev), historyValue(after)}
	}
	return out
}

// recordChange writes one entry. A failure is logged rather than returned:
// the change it describes has already been made.
func (t *typedResource[T]) recordChange(c *Context, key, action string, changes map[string][2]any) {
	if !t.historyOn() || key == "" {
		return
	}
	if action == ChangeUpdated && len(changes) == 0 {
		return
	}
	entry := RecordChange{Resource: t.res.m.slug, RecordKey: key, Action: action, TenantID: c.TenantID()}
	if c.User != nil {
		entry.UserID = c.User.ID
	}
	if len(changes) > 0 {
		raw, err := json.Marshal(changes)
		if err != nil {
			c.Panel.log.Warn("steward: encoding a history entry", "resource", t.res.m.slug, "err", err)
			return
		}
		entry.Changes = string(raw)
	}
	if err := c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Create(&entry).Error; err != nil {
		c.Panel.log.Warn("steward: writing a history entry", "resource", t.res.m.slug, "err", err)
	}
}

// recordChanges writes one entry without field changes for each key.
func (t *typedResource[T]) recordChanges(c *Context, keys []string, action string) {
	for _, k := range keys {
		t.recordChange(c, k, action, nil)
	}
}

// historyVM is one entry as the detail page shows it.
type historyVM struct {
	When    string
	User    string
	Action  string
	Changes []historyChangeVM
}

type historyChangeVM struct {
	Label  string
	Before string
	After  string
}

// historyLabels are what the detail page calls each action.
var historyLabels = map[string]string{
	ChangeCreated: "Created", ChangeUpdated: "Updated", ChangeDeleted: "Deleted",
	ChangeRestored: "Restored", ChangeForceDeleted: "Deleted permanently",
}

// historyVMs loads and shapes a record's latest entries for its detail page.
func (t *typedResource[T]) historyVMs(c *Context, key string) []historyVM {
	if !t.historyOn() {
		return nil
	}
	entries, err := c.Panel.History(c.Ctx(), t.res.m.slug, key, 20)
	if err != nil {
		c.Panel.log.Warn("steward: reading history", "resource", t.res.m.slug, "err", err)
		return nil
	}
	ids := map[uint]bool{}
	for _, e := range entries {
		if e.UserID != 0 {
			ids[e.UserID] = true
		}
	}
	names := map[uint]string{}
	if len(ids) > 0 {
		var users []User
		keys := make([]uint, 0, len(ids))
		for id := range ids {
			keys = append(keys, id)
		}
		if err := c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Select("id", "name", "username").
			Where("id IN ?", keys).Find(&users).Error; err == nil {
			for _, u := range users {
				names[u.ID] = u.Name
				if names[u.ID] == "" {
					names[u.ID] = u.Username
				}
			}
		}
	}
	out := make([]historyVM, 0, len(entries))
	for _, e := range entries {
		vm := historyVM{
			When:   e.CreatedAt.Format("2006-01-02 15:04"),
			User:   names[e.UserID],
			Action: c.lookup(historyLabels[e.Action]),
		}
		changes := e.FieldChanges()
		paths := make([]string, 0, len(changes))
		for p := range changes {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			ch := changes[p]
			vm.Changes = append(vm.Changes, historyChangeVM{
				Label: t.historyLabel(p), Before: historyText(ch.Before), After: historyText(ch.After),
			})
		}
		out = append(out, vm)
	}
	return out
}

// historyLabel is the label a change is shown under: the form's, else the
// field table's.
func (t *typedResource[T]) historyLabel(path string) string {
	if fd := t.form.fieldByPath(path); fd != nil && fd.label != "" {
		return fd.label
	}
	if info := t.ft.byPath[path]; info != nil {
		return info.Label
	}
	return path
}

// historyText renders a stored value for the page; an absent one reads as a
// dash.
func historyText(v any) string {
	if v == nil {
		return "—"
	}
	if s := fmt.Sprint(v); s != "" {
		return s
	}
	return "—"
}
