// Package migrate provides a small, versioned database migration runner on
// top of GORM. Migrations are Go functions registered in code (typically one
// file per migration in the application's migrations package), tracked in a
// records table with per-run batches, and executed transactionally.
//
// Steward's own framework tables are shipped as embedded migrations under the
// "core" source; applications register theirs under "app".
package migrate

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"gorm.io/gorm"
)

// Migration is a single reversible schema step. Name must be unique across
// all sources and should sort in execution order (e.g. "0001_create_users" or
// a "20260726150405_add_flags" timestamp prefix).
type Migration struct {
	Name string
	Up   func(tx *gorm.DB) error
	Down func(tx *gorm.DB) error
}

// Record is a row in the migrations table.
type Record struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255;uniqueIndex"`
	Source    string `gorm:"size:50;index"`
	Batch     int
	AppliedAt time.Time
}

// Status describes one known migration and whether it has been applied.
type Status struct {
	Name      string
	Source    string
	Applied   bool
	Batch     int
	AppliedAt time.Time
}

// Option configures a Runner.
type Option func(*Runner)

// WithTable overrides the records table name (default "admin_migrations").
func WithTable(name string) Option { return func(r *Runner) { r.table = name } }

// Runner executes registered migrations against one database.
type Runner struct {
	db      *gorm.DB
	table   string
	sources []string                // registration order
	byName  map[string]registration // name -> migration+source
}

type registration struct {
	Migration
	source string
}

// New creates a Runner. The records table is created lazily on first use.
func New(db *gorm.DB, opts ...Option) *Runner {
	r := &Runner{db: db, table: "admin_migrations", byName: map[string]registration{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Register adds migrations under a source label ("core", "app"). Within one
// Up run, all pending migrations execute grouped by source in registration
// order, sorted by name within each source. Duplicate names error at Up/Down.
func (r *Runner) Register(source string, ms ...Migration) {
	if len(ms) == 0 {
		return
	}
	if !slices.Contains(r.sources, source) {
		r.sources = append(r.sources, source)
	}
	for _, m := range ms {
		if prev, dup := r.byName[m.Name]; dup {
			// Poison the entry so Up/Down report it rather than silently
			// running one of the two.
			r.byName[m.Name] = registration{Migration: prev.Migration, source: duplicateMarker}
			continue
		}
		r.byName[m.Name] = registration{Migration: m, source: source}
	}
}

const duplicateMarker = "\x00duplicate"

func (r *Runner) recordsDB(ctx context.Context) (*gorm.DB, error) {
	db := r.db.WithContext(ctx).Table(r.table)
	if !r.db.Migrator().HasTable(r.table) {
		if err := r.db.WithContext(ctx).Table(r.table).AutoMigrate(&Record{}); err != nil {
			return nil, fmt.Errorf("migrate: creating records table %s: %w", r.table, err)
		}
	}
	return db, nil
}

// ordered returns all registrations in execution order, failing on duplicates.
func (r *Runner) ordered() ([]registration, error) {
	var out []registration
	for _, src := range r.sources {
		var group []registration
		for name, reg := range r.byName {
			if reg.source == duplicateMarker {
				return nil, fmt.Errorf("migrate: duplicate migration name %q", name)
			}
			if reg.source == src {
				group = append(group, reg)
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		out = append(out, group...)
	}
	return out, nil
}

func (r *Runner) applied(ctx context.Context) (map[string]Record, int, error) {
	db, err := r.recordsDB(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rows []Record
	if err := db.Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("migrate: reading records: %w", err)
	}
	m := make(map[string]Record, len(rows))
	maxBatch := 0
	for _, row := range rows {
		m[row.Name] = row
		if row.Batch > maxBatch {
			maxBatch = row.Batch
		}
	}
	return m, maxBatch, nil
}

// Up applies every pending migration, each inside its own transaction, and
// returns the names applied. All migrations applied by one call share a batch.
func (r *Runner) Up(ctx context.Context) ([]string, error) {
	regs, err := r.ordered()
	if err != nil {
		return nil, err
	}
	done, maxBatch, err := r.applied(ctx)
	if err != nil {
		return nil, err
	}
	batch := maxBatch + 1
	var appliedNames []string
	for _, reg := range regs {
		if _, ok := done[reg.Name]; ok {
			continue
		}
		if reg.Up == nil {
			return appliedNames, fmt.Errorf("migrate: %s has no Up", reg.Name)
		}
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := reg.Up(tx); err != nil {
				return err
			}
			rec := Record{Name: reg.Name, Source: reg.source, Batch: batch, AppliedAt: time.Now()}
			return tx.Table(r.table).Create(&rec).Error
		})
		if err != nil {
			return appliedNames, fmt.Errorf("migrate: applying %s: %w", reg.Name, err)
		}
		appliedNames = append(appliedNames, reg.Name)
	}
	return appliedNames, nil
}

// Down rolls back the most recent `steps` applied migrations (0 = the whole
// most recent batch), newest first, each in its own transaction.
func (r *Runner) Down(ctx context.Context, steps int) error {
	if _, err := r.ordered(); err != nil { // surface duplicate names early
		return err
	}
	done, maxBatch, err := r.applied(ctx)
	if err != nil {
		return err
	}
	var rows []Record
	for _, rec := range done {
		if steps == 0 && rec.Batch != maxBatch {
			continue
		}
		rows = append(rows, rec)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Batch != rows[j].Batch {
			return rows[i].Batch > rows[j].Batch
		}
		return rows[i].Name > rows[j].Name
	})
	if steps > 0 && steps < len(rows) {
		rows = rows[:steps]
	}
	for _, rec := range rows {
		reg, ok := r.byName[rec.Name]
		if !ok {
			return fmt.Errorf("migrate: %s is applied but not registered; cannot roll back", rec.Name)
		}
		if reg.Down == nil {
			return fmt.Errorf("migrate: %s has no Down", rec.Name)
		}
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := reg.Down(tx); err != nil {
				return err
			}
			return tx.Table(r.table).Where("name = ?", rec.Name).Delete(&Record{}).Error
		})
		if err != nil {
			return fmt.Errorf("migrate: rolling back %s: %w", rec.Name, err)
		}
	}
	return nil
}

// Status lists all registered migrations in execution order plus any applied
// records that are no longer registered (marked with source "?").
func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	regs, err := r.ordered()
	if err != nil {
		return nil, err
	}
	done, _, err := r.applied(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(regs))
	for _, reg := range regs {
		st := Status{Name: reg.Name, Source: reg.source}
		if rec, ok := done[reg.Name]; ok {
			st.Applied, st.Batch, st.AppliedAt = true, rec.Batch, rec.AppliedAt
			delete(done, reg.Name)
		}
		out = append(out, st)
	}
	orphans := make([]Status, 0, len(done))
	for _, rec := range done {
		orphans = append(orphans, Status{Name: rec.Name, Source: "?", Applied: true, Batch: rec.Batch, AppliedAt: rec.AppliedAt})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Name < orphans[j].Name })
	return append(out, orphans...), nil
}
