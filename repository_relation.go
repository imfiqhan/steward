package steward

// One-hop relation paths in SQL.
//
// A path like "Author.Name" or "Tags.Tag" is readable by reflection for display,
// but constraining a query on it needs real SQL. Every shape is expressed as
//
//	<owner column> IN (SELECT … FROM <related> WHERE <predicate>)
//
// rather than as a JOIN, because a join against a has-many or many-to-many
// relation multiplies the owner's rows: the grid's Count would over-report and a
// page of results would repeat rows. A subquery constrains without changing the
// row set, so counting and pagination stay correct for every relationship kind.

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// relTarget is the SQL topology of one relation path: the owner column to
// constrain, and where the matching values come from.
type relTarget struct {
	Name string                  // relationship name, e.g. "Tags"
	Kind schema.RelationshipType // belongs_to | has_one | has_many | many_to_many

	// LocalCol is the owner-table column the generated IN (…) constrains.
	LocalCol string

	// Table and Column locate the related field the predicate applies to.
	Table  string
	Column string

	// RemoteCol is the column the subquery selects: the related key for
	// belongs_to, the child's foreign key for has_one and has_many.
	RemoteCol string

	// Many-to-many only: the pivot table and the columns tying it to each side.
	JoinTable  string
	JoinLocal  string
	JoinRemote string
	RemoteKey  string // the related table's key the pivot points at

	// SoftDelete is the related table's soft-delete column, if it has one. The
	// subquery is hand-built SQL, so GORM's own soft-delete scope does not reach
	// it — without this, filtering by a deleted row would still match.
	SoftDelete string
}

var deletedAtType = reflect.TypeOf(gorm.DeletedAt{})

// softDeleteColumn returns a schema's soft-delete column, or "".
func softDeleteColumn(s *schema.Schema) string {
	for _, f := range s.Fields {
		if f.FieldType == deletedAtType && f.DBName != "" {
			return f.DBName
		}
	}
	return ""
}

// newRelTarget derives the topology for one relationship, reporting false when
// the shape is not something a single-column subquery can express — a composite
// key, or a relation GORM did not fully resolve. Callers turn that into a
// boot-time error rather than emitting SQL that would quietly match the wrong
// rows.
func newRelTarget(rel *schema.Relationship, related *schema.Field) (*relTarget, bool) {
	if rel.FieldSchema == nil || related.DBName == "" {
		return nil, false
	}
	rt := &relTarget{
		Name:       rel.Name,
		Kind:       rel.Type,
		Table:      rel.FieldSchema.Table,
		Column:     related.DBName,
		SoftDelete: softDeleteColumn(rel.FieldSchema),
	}

	if rel.Type == schema.Many2Many {
		if rel.JoinTable == nil {
			return nil, false
		}
		rt.JoinTable = rel.JoinTable.Table
		var ownerSide, relatedSide *schema.Reference
		for _, ref := range rel.References {
			// Both sides' foreign keys live on the pivot; OwnPrimaryKey marks
			// which one points back at the owner.
			if ref.OwnPrimaryKey {
				if ownerSide != nil {
					return nil, false // composite owner key
				}
				ownerSide = ref
				continue
			}
			if relatedSide != nil {
				return nil, false // composite related key
			}
			relatedSide = ref
		}
		if ownerSide == nil || relatedSide == nil ||
			ownerSide.PrimaryKey == nil || ownerSide.ForeignKey == nil ||
			relatedSide.PrimaryKey == nil || relatedSide.ForeignKey == nil {
			return nil, false
		}
		rt.LocalCol = ownerSide.PrimaryKey.DBName
		rt.JoinLocal = ownerSide.ForeignKey.DBName
		rt.JoinRemote = relatedSide.ForeignKey.DBName
		rt.RemoteKey = relatedSide.PrimaryKey.DBName
		return rt, true
	}

	if len(rel.References) != 1 {
		return nil, false // composite foreign key
	}
	ref := rel.References[0]
	if ref.PrimaryKey == nil || ref.ForeignKey == nil {
		return nil, false
	}
	if ref.OwnPrimaryKey {
		// has_one / has_many: the owner's key is matched by the child's FK.
		rt.LocalCol = ref.PrimaryKey.DBName
		rt.RemoteCol = ref.ForeignKey.DBName
	} else {
		// belongs_to: the owner holds the FK, matched against the related key.
		rt.LocalCol = ref.ForeignKey.DBName
		rt.RemoteCol = ref.PrimaryKey.DBName
	}
	if rt.LocalCol == "" || rt.RemoteCol == "" {
		return nil, false
	}
	return rt, true
}

// relCondSQL renders the "owner IN (subquery)" fragment constraining a relation
// path by cond, together with its arguments.
func (r *GormRepository[T]) relCondSQL(rt *relTarget, c Cond) (string, []any, error) {
	q := func(name string) string { return quoteColumn(r.db, name) }
	qual := func(table, col string) string { return q(table) + "." + q(col) }

	inner, args, err := predicateSQLFor(r.db.Name(), qual(rt.Table, rt.Column), c)
	if err != nil {
		return "", nil, err
	}
	if rt.SoftDelete != "" {
		inner += " AND " + qual(rt.Table, rt.SoftDelete) + " IS NULL"
	}

	owner := qual(r.ft.model.Table, rt.LocalCol)

	var sub string
	if rt.Kind == schema.Many2Many {
		sub = "SELECT " + qual(rt.JoinTable, rt.JoinLocal) +
			" FROM " + q(rt.JoinTable) +
			" JOIN " + q(rt.Table) +
			" ON " + qual(rt.Table, rt.RemoteKey) + " = " + qual(rt.JoinTable, rt.JoinRemote) +
			" WHERE " + inner
	} else {
		sub = "SELECT " + qual(rt.Table, rt.RemoteCol) +
			" FROM " + q(rt.Table) +
			" WHERE " + inner
	}
	return owner + " IN (" + sub + ")", args, nil
}

// predicateSQL renders one comparison against an already-quoted column.
//
// Splitting this out of applyCond is what lets a relation path reuse every
// operator: the same predicate goes either straight into the WHERE clause or
// inside a subquery.

// predicateSQLFor is predicateSQL against a named dialect.
//
// LIKE is what needs the dialect. MySQL compares it under a case-insensitive
// collation and SQLite folds ASCII case; PostgreSQL's LIKE is case-sensitive,
// so it takes ILIKE to match the other two.
func predicateSQLFor(dialect, col string, c Cond) (string, []any, error) {
	like := " LIKE ?"
	if dialect == "postgres" {
		like = " ILIKE ?"
	}
	switch c.Op {
	case OpEq:
		return col + " = ?", []any{c.Val}, nil
	case OpNe:
		return col + " <> ?", []any{c.Val}, nil
	case OpGt:
		return col + " > ?", []any{c.Val}, nil
	case OpGte:
		return col + " >= ?", []any{c.Val}, nil
	case OpLt:
		return col + " < ?", []any{c.Val}, nil
	case OpLte:
		return col + " <= ?", []any{c.Val}, nil
	case OpLike:
		return col + like, []any{"%" + fmt.Sprint(c.Val) + "%"}, nil
	case OpPrefix:
		return col + like, []any{fmt.Sprint(c.Val) + "%"}, nil
	case OpIn:
		return col + " IN ?", []any{c.Val}, nil
	case OpBetween:
		return col + " BETWEEN ? AND ?", []any{c.Val, c.Val2}, nil
	case OpNull:
		if b, _ := c.Val.(bool); !b {
			return col + " IS NOT NULL", nil, nil
		}
		return col + " IS NULL", nil, nil
	default:
		return "", nil, fmt.Errorf("steward: unsupported operator %q", c.Op)
	}
}
