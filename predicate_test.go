package steward

import "testing"

// PostgreSQL defines ILIKE (the ~~* operator) for text, name and character
// only, and nothing casts to those implicitly, so the column a pattern match
// reads has to be text by the time the parser resolves the operator. The other
// dialects take LIKE against any column type.
func TestLikePredicatePerDialect(t *testing.T) {
	const col = `"media_files"."submission_id"`

	for _, tc := range []struct {
		dialect string
		op      Op
		want    string
	}{
		{"postgres", OpLike, `CAST(` + col + ` AS TEXT) ILIKE ?`},
		{"postgres", OpPrefix, `CAST(` + col + ` AS TEXT) ILIKE ?`},
		{"mysql", OpLike, col + ` LIKE ?`},
		{"mysql", OpPrefix, col + ` LIKE ?`},
		{"sqlite", OpLike, col + ` LIKE ?`},
		{"sqlite", OpPrefix, col + ` LIKE ?`},
		{"sqlserver", OpLike, col + ` LIKE ?`},
		{"sqlserver", OpPrefix, col + ` LIKE ?`},
	} {
		t.Run(tc.dialect+"/"+string(tc.op), func(t *testing.T) {
			got, _, err := predicateSQLFor(tc.dialect, col, Cond{Path: "SubmissionID", Op: tc.op, Val: "x"})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("predicate = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every other operator compares against the column as it is. Equality on a uuid
// column resolves because the literal is coerced to the column's type; casting
// the column instead would compare text to text and put the primary key index
// out of reach.
func TestOnlyPatternMatchingCastsTheColumn(t *testing.T) {
	const col = `"media_files"."id"`

	for _, op := range []Op{OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpBetween, OpNull} {
		t.Run(string(op), func(t *testing.T) {
			got, _, err := predicateSQLFor("postgres", col, Cond{Path: "ID", Op: op, Val: "x", Val2: "y"})
			if err != nil {
				t.Fatal(err)
			}
			if want := col + " "; got[:len(want)] != want {
				t.Errorf("predicate = %q, want it to open with the bare column %q", got, col)
			}
		})
	}
}

// The pattern itself is unchanged by the cast: contains on both sides, prefix
// on one.
func TestPatternArgumentsAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		op   Op
		want string
	}{
		{OpLike, "%foto%"},
		{OpPrefix, "foto%"},
	} {
		_, args, err := predicateSQLFor("postgres", `"c"`, Cond{Op: tc.op, Val: "foto"})
		if err != nil {
			t.Fatal(err)
		}
		if len(args) != 1 || args[0] != tc.want {
			t.Errorf("%s args = %v, want [%q]", tc.op, args, tc.want)
		}
	}
}
