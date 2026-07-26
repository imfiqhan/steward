package steward

import "net/http"

// Envelope is the unified JSON response for every mutation and AJAX
// interaction, ported verbatim from dcat-admin so client behavior is uniform:
// admin.js interprets Data.Then to redirect, refresh, download, or run
// script after showing the toast.
type Envelope struct {
	Status bool                `json:"status"`
	Data   *EnvelopeData       `json:"data,omitempty"`
	HTML   string              `json:"html,omitempty"`
	Errors map[string][]string `json:"errors,omitempty"`

	code int
}

// EnvelopeData carries the toast and follow-up action.
type EnvelopeData struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"` // success | error | warning | info
	Alert   bool   `json:"alert,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Timeout int    `json:"timeout,omitempty"` // seconds; 0 = client default
	Then    *Then  `json:"then,omitempty"`
}

// Then names the client action performed after the toast.
type Then struct {
	Action string `json:"action"` // redirect | location | download | refresh | script
	Value  string `json:"value,omitempty"`
}

func newEnvelope(ok bool, typ, msg string) *Envelope {
	return &Envelope{Status: ok, Data: &EnvelopeData{Message: msg, Type: typ}, code: http.StatusOK}
}

// Success builds a success envelope.
func Success(msg string) *Envelope { return newEnvelope(true, "success", msg) }

// Error builds an error envelope.
func Error(msg string) *Envelope { return newEnvelope(false, "error", msg) }

// Warning builds a warning envelope.
func Warning(msg string) *Envelope { return newEnvelope(false, "warning", msg) }

// Info builds an info envelope.
func Info(msg string) *Envelope { return newEnvelope(true, "info", msg) }

// ValidationErrors builds the HTTP 422 envelope carrying per-field messages.
func ValidationErrors(errs map[string][]string) *Envelope {
	return &Envelope{Status: false, Errors: errs, code: http.StatusUnprocessableEntity}
}

// Code overrides the HTTP status (default 200, or 422 for ValidationErrors).
func (e *Envelope) Code(status int) *Envelope { e.code = status; return e }

// Detail adds secondary toast text.
func (e *Envelope) Detail(s string) *Envelope { e.Data.Detail = s; return e }

// Alert renders a blocking dialog instead of a toast.
func (e *Envelope) Alert() *Envelope { e.Data.Alert = true; return e }

// Redirect navigates via the HTMX-aware client router after the toast.
func (e *Envelope) Redirect(url string) *Envelope {
	e.Data.Then = &Then{Action: "redirect", Value: url}
	return e
}

// Location performs a full browser navigation.
func (e *Envelope) Location(url string) *Envelope {
	e.Data.Then = &Then{Action: "location", Value: url}
	return e
}

// Refresh reloads the current view.
func (e *Envelope) Refresh() *Envelope {
	e.Data.Then = &Then{Action: "refresh"}
	return e
}

// Download triggers a file download.
func (e *Envelope) Download(url string) *Envelope {
	e.Data.Then = &Then{Action: "download", Value: url}
	return e
}

// Script runs a JS snippet (trusted, author-supplied) after the toast.
func (e *Envelope) Script(js string) *Envelope {
	e.Data.Then = &Then{Action: "script", Value: js}
	return e
}
