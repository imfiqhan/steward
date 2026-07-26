package steward

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Action is a custom operation on a resource, rendered as a button and
// dispatched to POST {resource}/_action/{name}. Row actions receive the
// clicked row's id, batch actions the selection, tool actions no ids.
type Action struct {
	name    string
	label   string
	icon    string
	confirm string
	danger  bool
	handler func(c *Context, ids []string) (*Envelope, error)
}

// NewAction builds an action; name must be a slug (letters, digits, - _).
// The handler's returned envelope drives the client (toast, refresh,
// redirect, download); returning nil means Success + Refresh.
func NewAction(name, label string, handler func(c *Context, ids []string) (*Envelope, error)) *Action {
	return &Action{name: name, label: label, handler: handler}
}

// Icon sets the button's Tabler icon.
func (a *Action) Icon(name string) *Action { a.icon = name; return a }

// Confirm asks before dispatching.
func (a *Action) Confirm(message string) *Action { a.confirm = message; return a }

// Danger styles the button red.
func (a *Action) Danger() *Action { a.danger = true; return a }

var actionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RowAction adds a per-row action button.
func (g *Grid[T]) RowAction(a *Action) *Grid[T] {
	g.rowActions = append(g.rowActions, a)
	return g
}

// BatchAction adds an action over the selected rows.
func (g *Grid[T]) BatchAction(a *Action) *Grid[T] {
	g.batchActions = append(g.batchActions, a)
	return g
}

// ToolAction adds a toolbar action (no row context).
func (g *Grid[T]) ToolAction(a *Action) *Grid[T] {
	g.toolActions = append(g.toolActions, a)
	return g
}

func (g *Grid[T]) allActions() []*Action {
	out := make([]*Action, 0, len(g.rowActions)+len(g.batchActions)+len(g.toolActions))
	out = append(out, g.rowActions...)
	out = append(out, g.batchActions...)
	return append(out, g.toolActions...)
}

// verifyActions runs at compile: names must be unique slugs with handlers.
func (t *typedResource[T]) verifyActions(a *Admin) {
	seen := map[string]bool{}
	for _, act := range t.grid.allActions() {
		switch {
		case !actionNameRe.MatchString(act.name):
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: action name %q must match [A-Za-z0-9_-]+", t.res.m.slug, act.name))
		case act.handler == nil:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: action %q has no handler", t.res.m.slug, act.name))
		case seen[act.name]:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: duplicate action name %q", t.res.m.slug, act.name))
		}
		seen[act.name] = true
	}
}

// dispatchAction handles POST {base}/_action/{name}.
func (t *typedResource[T]) dispatchAction(c *Context) error {
	name := c.R.PathValue("name")
	var action *Action
	for _, act := range t.grid.allActions() {
		if act.name == name {
			action = act
			break
		}
	}
	if action == nil {
		return c.Envelope(Error("Unknown action.").Code(http.StatusNotFound))
	}
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	var ids []string
	for _, raw := range c.R.Form["ids"] {
		for id := range strings.SplitSeq(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	env, err := action.handler(c, ids)
	if err != nil {
		return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
	}
	if env == nil {
		env = Success(action.label + " done.").Refresh()
	}
	return c.Envelope(env)
}

// actionVM feeds the grid templates.
type actionVM struct {
	Name    string
	Label   string
	Icon    string
	Confirm string
	Danger  bool
	URL     string
}

func actionVMs(base string, actions []*Action) []actionVM {
	out := make([]actionVM, 0, len(actions))
	for _, a := range actions {
		out = append(out, actionVM{
			Name: a.name, Label: a.label, Icon: a.icon,
			Confirm: a.confirm, Danger: a.danger,
			URL: base + "/_action/" + a.name,
		})
	}
	return out
}
