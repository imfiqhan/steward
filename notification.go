package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notification is a message addressed to one panel account, stored in the
// database and read from the bell in the header.
//
// Title, Body, URL and Icon are what the panel renders. Data is for the
// caller: an arbitrary JSON payload the panel never interprets, so a handler
// reading a notification back can recover what it was about without parsing
// the prose.
type Notification struct {
	ID     uint   `gorm:"primaryKey"`
	UserID uint   `gorm:"not null;index:idx_notif_user_read,priority:1"`
	Type   string `gorm:"size:120;index"`
	Title  string `gorm:"size:255;not null"`
	Body   string `gorm:"type:text"`
	URL    string `gorm:"size:512"`
	Icon   string `gorm:"size:60"`
	Data   string `gorm:"type:text"`

	// Null until read. Indexed with UserID because every query here is "this
	// user's, unread first".
	ReadAt    *time.Time `gorm:"index:idx_notif_user_read,priority:2"`
	CreatedAt time.Time
}

func (Notification) TableName() string { return prefixed("notifications") }

// Read reports whether the notification has been read.
func (n *Notification) Read() bool { return n.ReadAt != nil }

// Payload unmarshals Data into v. It is a no-op when Data is empty, so a
// notification stored without one does not have to be special-cased.
func (n *Notification) Payload(v any) error {
	if n.Data == "" {
		return nil
	}
	return json.Unmarshal([]byte(n.Data), v)
}

// WithPayload marshals v into Data and returns the notification, for use
// inline in a Notify call.
func (n Notification) WithPayload(v any) Notification {
	b, err := json.Marshal(v)
	if err != nil {
		// A payload that cannot be marshalled is a programming error, and
		// losing the notification over it would be worse than losing the
		// payload; the error is recorded in Data's place.
		n.Data = fmt.Sprintf("{%q:%q}", "error", err.Error())
		return n
	}
	n.Data = string(b)
	return n
}

// notificationListLimit caps what the bell shows. Older ones stay in the
// table; nothing here pages, because a panel's bell is a recent-activity
// view, not an archive.
const notificationListLimit = 15

// Notify stores a notification for one account.
//
// It writes a row and returns; nothing is delivered out of process, so a
// caller in a request handler pays one INSERT. ID, UserID and CreatedAt are
// set by this call and need not be filled in.
func (a *Admin) Notify(ctx context.Context, userID uint, n Notification) error {
	if userID == 0 {
		return errors.New("steward: Notify needs a user ID")
	}
	if n.Title == "" {
		return errors.New("steward: a notification needs a Title")
	}
	n.ID, n.UserID, n.ReadAt = 0, userID, nil
	n.CreatedAt = time.Time{}
	return a.db.WithContext(ctx).Create(&n).Error
}

// NotifyUsers stores the same notification for several accounts in one
// statement. Duplicate and zero IDs are dropped.
func (a *Admin) NotifyUsers(ctx context.Context, userIDs []uint, n Notification) error {
	if n.Title == "" {
		return errors.New("steward: a notification needs a Title")
	}
	seen := make(map[uint]bool, len(userIDs))
	rows := make([]Notification, 0, len(userIDs))
	for _, id := range userIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		row := n
		row.ID, row.UserID, row.ReadAt = 0, id, nil
		row.CreatedAt = time.Time{}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return a.db.WithContext(ctx).Create(&rows).Error
}

// NotifyRole stores a notification for every account holding any of the given
// roles. Accounts holding two of them are notified once.
//
// The administrator role is not implicit here: it short-circuits permission
// checks, not delivery, so notifying "editor" does not reach an administrator
// who is not one.
func (a *Admin) NotifyRole(ctx context.Context, n Notification, roleSlugs ...string) error {
	if len(roleSlugs) == 0 {
		return errors.New("steward: NotifyRole needs at least one role")
	}
	users := prefixed("users")
	var ids []uint
	err := a.db.WithContext(ctx).
		Model(&AdminUser{}).
		Distinct(users+".id").
		Joins("JOIN "+prefixed("role_users")+" ru ON ru.user_id = "+users+".id").
		Joins("JOIN "+prefixed("roles")+" r ON r.id = ru.role_id").
		Where("r.slug IN ?", roleSlugs).
		Pluck(users+".id", &ids).Error
	if err != nil {
		return fmt.Errorf("finding the accounts to notify: %w", err)
	}
	return a.NotifyUsers(ctx, ids, n)
}

// Notifications returns an account's most recent notifications, unread first.
func (a *Admin) Notifications(ctx context.Context, userID uint, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = notificationListLimit
	}
	var out []Notification
	err := a.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("read_at IS NULL DESC, created_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// UnreadNotifications counts an account's unread notifications.
func (a *Admin) UnreadNotifications(ctx context.Context, userID uint) (int64, error) {
	var n int64
	err := a.db.WithContext(ctx).
		Model(&Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(&n).Error
	return n, err
}

// MarkNotificationRead marks one notification read. The user ID is part of the
// statement, so one account cannot mark another's.
func (a *Admin) MarkNotificationRead(ctx context.Context, userID, id uint) error {
	return a.db.WithContext(ctx).
		Model(&Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Update("read_at", time.Now()).Error
}

// MarkNotificationsRead marks every unread notification of an account read.
func (a *Admin) MarkNotificationsRead(ctx context.Context, userID uint) error {
	return a.db.WithContext(ctx).
		Model(&Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", time.Now()).Error
}

// DeleteNotification removes one of an account's notifications.
func (a *Admin) DeleteNotification(ctx context.Context, userID, id uint) error {
	return a.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&Notification{}).Error
}

// PruneNotifications deletes read notifications older than age, and returns
// how many went. Unread ones are always kept, however old.
//
// Nothing calls this for you: the table grows until something does. Run it
// from a cron entry or the app's own scheduler.
func (a *Admin) PruneNotifications(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, errors.New("steward: PruneNotifications needs a positive age")
	}
	res := a.db.WithContext(ctx).
		Where("read_at IS NOT NULL AND created_at < ?", time.Now().Add(-age)).
		Delete(&Notification{})
	return res.RowsAffected, res.Error
}

// notificationsEnabled reports whether the bell is rendered and its endpoints
// are mounted.
func (a *Admin) notificationsEnabled() bool { return !a.cfg.DisableNotifications }
