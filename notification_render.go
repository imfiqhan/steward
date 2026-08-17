package steward

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// notificationVM is one row as the template needs it.
type notificationVM struct {
	ID    uint
	Title string
	Body  string
	URL   string
	Icon  string
	When  string
	Read  bool
}

type notificationsVM struct {
	Items  []notificationVM
	Unread int64
}

// notificationBell renders the bell's badge. Polled, so it stays cheap: one
// indexed count, no list.
func (a *Admin) notificationBell(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	n, err := a.UnreadNotifications(c.Ctx(), c.User.ID)
	if err != nil {
		return err
	}
	return a.renderFragment(c, "layout/notification_badge.html", notificationsVM{Unread: n})
}

// notificationList renders the panel behind the bell, fetched when it opens.
func (a *Admin) notificationList(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	items, err := a.Notifications(c.Ctx(), c.User.ID, notificationListLimit)
	if err != nil {
		return err
	}
	unread, err := a.UnreadNotifications(c.Ctx(), c.User.ID)
	if err != nil {
		return err
	}
	vm := notificationsVM{Unread: unread, Items: make([]notificationVM, 0, len(items))}
	for i := range items {
		it := &items[i]
		vm.Items = append(vm.Items, notificationVM{
			ID:    it.ID,
			Title: it.Title,
			Body:  it.Body,
			URL:   it.URL,
			Icon:  it.Icon,
			When:  relativeTime(it.CreatedAt),
			Read:  it.Read(),
		})
	}
	return a.renderFragment(c, "layout/notification_list.html", vm)
}

// notificationRead marks one read and answers with the refreshed list, so the
// row and the badge update from one request.
func (a *Admin) notificationRead(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	id, err := strconv.ParseUint(c.R.PathValue("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, Error("Unknown notification."))
	}
	if err := a.MarkNotificationRead(c.Ctx(), c.User.ID, uint(id)); err != nil {
		return err
	}
	return a.notificationList(c)
}

// notificationGo marks one read and sends the browser to what it is about, so
// a row with a URL can be a plain link: no script, and middle-click and
// keyboard activation behave the way a link should.
func (a *Admin) notificationGo(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	id, err := strconv.ParseUint(c.R.PathValue("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, Error("Unknown notification."))
	}
	var n Notification
	if err := a.db.WithContext(c.Ctx()).
		Where("id = ? AND user_id = ?", uint(id), c.User.ID).
		First(&n).Error; err != nil {
		return c.JSON(http.StatusNotFound, Error("Unknown notification."))
	}
	if err := a.MarkNotificationRead(c.Ctx(), c.User.ID, n.ID); err != nil {
		return err
	}
	return c.Redirect(localPath(n.URL, a.url("/")))
}

// localPath keeps a stored URL from becoming an open redirect: only a path on
// this origin is followed, and "//host" is a path only by appearance.
func localPath(raw, fallback string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return fallback
	}
	return raw
}

// notificationReadAll marks the account's unread notifications read.
func (a *Admin) notificationReadAll(c *Context) error {
	if c.User == nil {
		return c.JSON(http.StatusUnauthorized, Error("Sign in first."))
	}
	if err := a.MarkNotificationsRead(c.Ctx(), c.User.ID); err != nil {
		return err
	}
	return a.notificationList(c)
}

// renderFragment writes one template with no surrounding layout.
//
// Rendered into a buffer first: a template that fails halfway has already
// written a 200 and part of a body, and the error page then appended to it
// arrives as a fragment, so the client swaps a whole page into a menu.
func (a *Admin) renderFragment(c *Context, name string, data any) error {
	var buf bytes.Buffer
	if err := a.renderer.execute(&buf, name, a.pageMetaFor(c, ""), data); err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.Header().Set("Cache-Control", "no-store")
	_, err := c.W.Write(buf.Bytes())
	return err
}

// relativeTime is a short "how long ago", which is what a notification list
// wants; anything older than a week reads as a date.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	default:
		return t.Format("2 Jan 2006")
	}
}
