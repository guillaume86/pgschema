// Package refactor reads the rename directives of a .pgmodel's "refactor"
// block and applies them by inspection: the merp fork's rename patch (see
// fork/README.md). The engine renames exactly what the block lists and infers
// nothing: no cascade to a constraint or an index that is not listed, no
// guess from a drop and an add of equal shape.
package refactor

import (
	"fmt"
	"strings"
)

// OpRename is the only operation the block carries.
const OpRename = "rename"

// The kinds of object a rename entry names.
const (
	KindTable      = "table"
	KindColumn     = "column"
	KindConstraint = "constraint"
	KindIndex      = "index"
)

// Entry is one directive, schema-qualified as merp writes it:
//
//	table      merp.todo_subtask                       -> merp.todo_step
//	column     merp.todo_todo_item.notes               -> merp.todo_todo_item.description
//	constraint merp.todo_step.todo_subtask_pkey        -> merp.todo_step.todo_step_pkey
//	index      merp.todo_subtask_label_idx             -> merp.todo_step_label_idx
//
// A column or constraint path names its table as the block has renamed it so
// far: entries apply in the block's order.
type Entry struct {
	Op   string `json:"op"`
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Result is an entry with whether it did anything: the plan JSON's "refactor"
// array holds one per entry, in the block's order.
type Result struct {
	Entry
	Applied bool `json:"applied"`
}

// path is an entry's from or to, split into schema, owner table (column and
// constraint only) and leaf.
type path struct{ schema, table, leaf string }

func (e Entry) String() string {
	return fmt.Sprintf("%s %s to %s", e.Kind, e.From, e.To)
}

// Validate refuses, by name, an unknown op or kind, a malformed path, and a
// rename that changes more than the leaf.
func (e Entry) Validate() error {
	if e.Op != OpRename {
		return fmt.Errorf("refactor entry %q: unsupported op %q (only %q)", e.From, e.Op, OpRename)
	}
	from, to, err := e.paths()
	if err != nil {
		return err
	}
	if from.schema != to.schema || from.table != to.table {
		return fmt.Errorf("refactor entry %s: a rename changes only the last name, not %q to %q", e, e.From, e.To)
	}
	if from.leaf == to.leaf {
		return fmt.Errorf("refactor entry %s: old and new names are equal", e)
	}
	return nil
}

// ValidateAll validates every entry.
func ValidateAll(entries []Entry) error {
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e Entry) paths() (from, to path, err error) {
	var want int
	switch e.Kind {
	case KindTable, KindIndex:
		want = 2
	case KindColumn, KindConstraint:
		want = 3
	default:
		return from, to, fmt.Errorf("refactor entry %q: unsupported kind %q (table, column, constraint or index)", e.From, e.Kind)
	}
	if from, err = parsePath(e, e.From, want); err != nil {
		return from, to, err
	}
	to, err = parsePath(e, e.To, want)
	return from, to, err
}

func parsePath(e Entry, s string, want int) (path, error) {
	parts := strings.Split(s, ".")
	if len(parts) != want {
		return path{}, fmt.Errorf("refactor entry %s: %q is not a schema-qualified %s name (%d dot-separated parts)", e, s, e.Kind, want)
	}
	for _, p := range parts {
		if p == "" {
			return path{}, fmt.Errorf("refactor entry %s: %q has an empty name part", e, s)
		}
	}
	if want == 2 {
		return path{schema: parts[0], leaf: parts[1]}, nil
	}
	return path{schema: parts[0], table: parts[1], leaf: parts[2]}, nil
}
