package refactor

import (
	"fmt"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/ir"
)

// Apply walks the entries in order against the inspected current state. For
// each: old present and new absent, the rename statement is returned as a
// diff to run first in the plan; old absent, nothing happens (a fresh
// database, or one already renamed); both present, an error naming the entry.
//
// current is moved by name only (a table's, a column's, a constraint's with
// the index behind a primary key or unique constraint, an index's), so a later
// entry finds what an earlier one renamed: it decides the entries and is no
// state to diff. What a rename changes elsewhere (a CHECK, an index predicate,
// a view, a foreign key) is the server's to say: the plan runs the statements
// in its session and reads the catalogue back. schema is the plan's schema: an
// entry naming another one is refused.
func Apply(current *ir.IR, schema string, entries []Entry) ([]diff.Diff, []Result, error) {
	return walk(current, schema, entries, nil)
}

// RefuseOnDocument checks the entries against a current state read from a
// document (plan --source): a plan between two documents renames nothing, so
// an entry whose old object the document holds is refused by name; an absent
// one is inert, as in Apply.
func RefuseOnDocument(current *ir.IR, schema string, entries []Entry) ([]Result, error) {
	_, results, err := walk(current, schema, entries, func(e Entry) error {
		return fmt.Errorf("refused: rename %s: a plan between two documents renames nothing. Nothing was planned.", e)
	})
	return results, err
}

// walk is Apply; refuse, when set, is the error for an entry whose old object
// is present, before the both-exist check.
func walk(current *ir.IR, schema string, entries []Entry, refuse func(Entry) error) ([]diff.Diff, []Result, error) {
	if err := ValidateAll(entries); err != nil {
		return nil, nil, err
	}
	s := current.Schemas[schema]
	if s == nil {
		return nil, nil, fmt.Errorf("current state has no schema %q", schema)
	}
	var diffs []diff.Diff
	results := make([]Result, 0, len(entries))
	for _, e := range entries {
		from, to, _ := e.paths()
		if from.schema != schema {
			return nil, nil, fmt.Errorf("refactor entry %s: schema %q is not the planned schema %q", e, from.schema, schema)
		}
		var r *present
		switch e.Kind {
		case KindTable:
			r = renameTable(s, from, to)
		case KindColumn:
			r = renameColumn(s, from, to)
		case KindConstraint:
			r = renameConstraint(s, from, to)
		case KindIndex:
			r = renameIndex(s, from, to)
		}
		if r != nil {
			if refuse != nil {
				return nil, nil, refuse(e)
			}
			if r.taken {
				return nil, nil, fmt.Errorf("refused: rename %s: both exist. Nothing was planned.", e)
			}
			r.move()
			diffs = append(diffs, *r.step)
		}
		results = append(results, Result{Entry: e, Applied: r != nil})
	}
	return diffs, results, nil
}

// present is an entry whose old object is present: whether the new name is
// taken, the in-memory move of the names, and the statement.
type present struct {
	taken bool
	move  func()
	step  *diff.Diff
}

func step(typ diff.DiffType, p string, sql string) *diff.Diff {
	return &diff.Diff{
		Type:       typ,
		Operation:  diff.DiffOperationAlter,
		Path:       p,
		Statements: []diff.SQLStatement{{SQL: sql, CanRunInTransaction: true}},
	}
}

func q(schema, name string) string {
	return ir.QuoteIdentifier(schema) + "." + ir.QuoteIdentifier(name)
}

// relationExists reports whether the schema already holds a relation named
// name: PostgreSQL's one namespace for tables, views, sequences and indexes.
func relationExists(s *ir.Schema, name string) bool {
	if s.Tables[name] != nil || s.Views[name] != nil || s.Sequences[name] != nil {
		return true
	}
	for _, t := range s.Tables {
		if t.Indexes[name] != nil {
			return true
		}
	}
	return false
}

func renameTable(s *ir.Schema, from, to path) *present {
	t := s.Tables[from.leaf]
	if t == nil {
		return nil
	}
	return &present{
		taken: relationExists(s, to.leaf),
		move: func() {
			delete(s.Tables, from.leaf)
			t.Name = to.leaf
			s.Tables[to.leaf] = t
		},
		step: step(diff.DiffTypeTable, from.schema+"."+to.leaf,
			fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", q(from.schema, from.leaf), ir.QuoteIdentifier(to.leaf))),
	}
}

func renameColumn(s *ir.Schema, from, to path) *present {
	t := s.Tables[from.table]
	if t == nil {
		return nil
	}
	var col *ir.Column
	taken := false
	for _, c := range t.Columns {
		switch c.Name {
		case from.leaf:
			col = c
		case to.leaf:
			taken = true
		}
	}
	if col == nil {
		return nil
	}
	return &present{
		taken: taken,
		move:  func() { col.Name = to.leaf },
		step: step(diff.DiffTypeTableColumn, from.schema+"."+from.table+"."+to.leaf,
			fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", q(from.schema, from.table), ir.QuoteIdentifier(from.leaf), ir.QuoteIdentifier(to.leaf))),
	}
}

func renameConstraint(s *ir.Schema, from, to path) *present {
	t := s.Tables[from.table]
	if t == nil {
		return nil
	}
	c := t.Constraints[from.leaf]
	if c == nil {
		return nil
	}
	return &present{
		taken: t.Constraints[to.leaf] != nil || t.Indexes[to.leaf] != nil,
		move: func() {
			delete(t.Constraints, from.leaf)
			c.Name = to.leaf
			t.Constraints[to.leaf] = c
			// PostgreSQL renames the index behind a primary key or unique
			// constraint with it; the inspected state may list that index too.
			if i := t.Indexes[from.leaf]; i != nil && (c.Type == ir.ConstraintTypePrimaryKey || c.Type == ir.ConstraintTypeUnique) {
				delete(t.Indexes, from.leaf)
				i.Name = to.leaf
				t.Indexes[to.leaf] = i
			}
		},
		step: step(diff.DiffTypeTableConstraint, from.schema+"."+from.table+"."+to.leaf,
			fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s;", q(from.schema, from.table), ir.QuoteIdentifier(from.leaf), ir.QuoteIdentifier(to.leaf))),
	}
}

func renameIndex(s *ir.Schema, from, to path) *present {
	var owner *ir.Table
	for _, t := range s.Tables {
		if t.Indexes[from.leaf] != nil {
			owner = t
		}
	}
	if owner == nil {
		return nil
	}
	return &present{
		taken: relationExists(s, to.leaf),
		move: func() {
			i := owner.Indexes[from.leaf]
			delete(owner.Indexes, from.leaf)
			i.Name = to.leaf
			owner.Indexes[to.leaf] = i
		},
		step: step(diff.DiffTypeTableIndex, from.schema+"."+owner.Name+"."+to.leaf,
			fmt.Sprintf("ALTER INDEX %s RENAME TO %s;", q(from.schema, from.leaf), ir.QuoteIdentifier(to.leaf))),
	}
}
