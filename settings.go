package steward

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const settingsCacheKey = "steward:setting:"

// Setting returns the stored value for slug ("" when absent), cached.
func (a *Admin) Setting(ctx context.Context, slug string) (string, error) {
	if b, ok, _ := a.cfg.Cache.Get(ctx, settingsCacheKey+slug); ok {
		return string(b), nil
	}
	var s Setting
	err := a.db.WithContext(ctx).First(&s, "slug = ?", slug).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_ = a.cfg.Cache.Set(ctx, settingsCacheKey+slug, []byte(s.Value), 10*time.Minute)
	return s.Value, nil
}

// SetSetting upserts a KV row and refreshes the cache.
func (a *Admin) SetSetting(ctx context.Context, slug, value string) error {
	s := Setting{Slug: slug, Value: value}
	err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "slug"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&s).Error
	if err != nil {
		return err
	}
	return a.cfg.Cache.Set(ctx, settingsCacheKey+slug, []byte(value), 10*time.Minute)
}
