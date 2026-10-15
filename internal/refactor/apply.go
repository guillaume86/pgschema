package refactor

import (
	"fmt"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/ir"
)

// Apply walks the entries in order against the inspected current state. For
// each: old present and new absent, the current-state IR is renamed in memory
// and the rename statement is returned as a diff to run first in the plan;
// old absent, nothing happens (a fresh database, or one already renamed); both
// present, an error naming the entry. Nothing else moves: a constraint or an
// index the block does not list keeps its name and is diffed as the model says.
//
// current is mutated. schema is the plan's schema: an entry naming another one
// is refused.
func Apply(current *ir.IR, schema string, entries []Entry) ([]diff.Diff, []Result, error) {
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
		var d *diff.Diff
		var err error
		switch e.Kind {
		case KindTable:
			d, err = renameTable(s, e, from, to)
		case KindColumn:
			d, err = renameColumn(s, e, from, to)
		case KindConstraint:
			d, err = renameConstraint(s, e, from, to)
		case KindIndex:
			d, err = renameIndex(s, e, from, to)
		}
		if err != nil {
			return nil, nil, err
		}
		if d != nil {
			diffs = append(diffs, *d)
		}
		results = append(results, Result{Entry: e, Applied: d != nil})
	}
	return diffs, results, nil
}

func bothExist(e Entry) error {
	return fmt.Errorf("refused: rename %s: both exist. Nothing was planned.", e)
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

func renameTable(s *ir.Schema, e Entry, from, to path) (*diff.Diff, error) {
	t := s.Tables[from.leaf]
	if t == nil {
		return nil, nil
	}
	if relationExists(s, to.leaf) {
		return nil, bothExist(e)
	}
	delete(s.Tables, from.leaf)
	t.Name = to.leaf
	s.Tables[to.leaf] = t
	for _, c := range t.Constraints {
		c.Table = to.leaf
	}
	for _, i := range t.Indexes {
		i.Table = to.leaf
	}
	for _, tr := range t.Triggers {
		tr.Table = to.leaf
	}
	for _, p := range t.Policies {
		p.Table = to.leaf
	}
	for _, o := range s.Tables {
		for _, c := range o.Constraints {
			if c.ReferencedSchema == from.schema && c.ReferencedTable == from.leaf {
				c.ReferencedTable = to.leaf
			}
		}
		for k := range o.Dependencies {
			if d := &o.Dependencies[k]; d.Schema == from.schema && d.Name == from.leaf {
				d.Name = to.leaf
			}
		}
	}
	return step(diff.DiffTypeTable, from.schema+"."+to.leaf,
		fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", q(from.schema, from.leaf), ir.QuoteIdentifier(to.leaf))), nil
}

func renameColumn(s *ir.Schema, e Entry, from, to path) (*diff.Diff, error) {
	t := s.Tables[from.table]
	if t == nil {
		return nil, nil
	}
	var col *ir.Column
	for _, c := range t.Columns {
		if c.Name == from.leaf {
			col = c
		}
	}
	if col == nil {
		return nil, nil
	}
	for _, c := range t.Columns {
		if c.Name == to.leaf {
			return nil, bothExist(e)
		}
	}
	col.Name = to.leaf
	renameCols := func(cs []*ir.ConstraintColumn) {
		for _, c := range cs {
			if c.Name == from.leaf {
				c.Name = to.leaf
			}
		}
	}
	for _, c := range t.Constraints {
		renameCols(c.Columns)
		if c.CheckClause != "" {
			c.CheckClause = replaceIdent(c.CheckClause, from.leaf, to.leaf)
		}
		for k, n := range c.DeleteSetColumns {
			if n == from.leaf {
				c.DeleteSetColumns[k] = to.leaf
			}
		}
	}
	for _, o := range s.Tables {
		for _, c := range o.Constraints {
			if c.ReferencedSchema == from.schema && c.ReferencedTable == from.table {
				renameCols(c.ReferencedColumns)
			}
		}
	}
	for _, i := range t.Indexes {
		for _, c := range i.Columns {
			if c.Name == from.leaf {
				c.Name = to.leaf
			}
		}
		for k, n := range i.IncludeColumns {
			if n == from.leaf {
				i.IncludeColumns[k] = to.leaf
			}
		}
		if i.Where != "" {
			i.Where = replaceIdent(i.Where, from.leaf, to.leaf)
		}
	}
	for _, c := range t.Columns {
		if c.GeneratedExpr != nil {
			x := replaceIdent(*c.GeneratedExpr, from.leaf, to.leaf)
			c.GeneratedExpr = &x
		}
	}
	return step(diff.DiffTypeTableColumn, from.schema+"."+from.table+"."+to.leaf,
		fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", q(from.schema, from.table), ir.QuoteIdentifier(from.leaf), ir.QuoteIdentifier(to.leaf))), nil
}

func renameConstraint(s *ir.Schema, e Entry, from, to path) (*diff.Diff, error) {
	t := s.Tables[from.table]
	if t == nil {
		return nil, nil
	}
	c := t.Constraints[from.leaf]
	if c == nil {
		return nil, nil
	}
	if t.Constraints[to.leaf] != nil || t.Indexes[to.leaf] != nil {
		return nil, bothExist(e)
	}
	delete(t.Constraints, from.leaf)
	c.Name = to.leaf
	t.Constraints[to.leaf] = c
	// PostgreSQL renames the index behind a primary key or unique constraint
	// with it; the inspected state may list that index too.
	if i := t.Indexes[from.leaf]; i != nil && (c.Type == ir.ConstraintTypePrimaryKey || c.Type == ir.ConstraintTypeUnique) {
		delete(t.Indexes, from.leaf)
		i.Name = to.leaf
		t.Indexes[to.leaf] = i
	}
	return step(diff.DiffTypeTableConstraint, from.schema+"."+from.table+"."+to.leaf,
		fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s;", q(from.schema, from.table), ir.QuoteIdentifier(from.leaf), ir.QuoteIdentifier(to.leaf))), nil
}

func renameIndex(s *ir.Schema, e Entry, from, to path) (*diff.Diff, error) {
	var owner *ir.Table
	for _, t := range s.Tables {
		if t.Indexes[from.leaf] != nil {
			owner = t
		}
	}
	if owner == nil {
		return nil, nil
	}
	if relationExists(s, to.leaf) {
		return nil, bothExist(e)
	}
	i := owner.Indexes[from.leaf]
	delete(owner.Indexes, from.leaf)
	i.Name = to.leaf
	owner.Indexes[to.leaf] = i
	return step(diff.DiffTypeTableIndex, from.schema+"."+owner.Name+"."+to.leaf,
		fmt.Sprintf("ALTER INDEX %s RENAME TO %s;", q(from.schema, from.leaf), ir.QuoteIdentifier(to.leaf))), nil
}
