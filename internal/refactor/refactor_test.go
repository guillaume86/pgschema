package refactor

import (
	"strings"
	"testing"

	"github.com/pgplex/pgschema/ir"
)

// current builds the inspected state of the v2 release: todo_subtask with its
// primary key, a foreign key to todo_todo_item, a tenant index and a label
// index, and todo_todo_item with a notes column that a check and an index read.
func current() *ir.IR {
	cols := func(names ...string) []*ir.ConstraintColumn {
		var out []*ir.ConstraintColumn
		for i, n := range names {
			out = append(out, &ir.ConstraintColumn{Name: n, Position: i + 1})
		}
		return out
	}
	item := &ir.Table{
		Schema: "merp", Name: "todo_todo_item",
		Columns: []*ir.Column{{Name: "id"}, {Name: "notes"}},
		Constraints: map[string]*ir.Constraint{
			"todo_todo_item_pkey":      {Schema: "merp", Table: "todo_todo_item", Name: "todo_todo_item_pkey", Type: ir.ConstraintTypePrimaryKey, Columns: cols("id")},
			"todo_todo_item_notes_chk": {Schema: "merp", Table: "todo_todo_item", Name: "todo_todo_item_notes_chk", Type: ir.ConstraintTypeCheck, CheckClause: `(char_length(notes) < 20 AND notes <> 'notes')`},
		},
		Indexes: map[string]*ir.Index{
			"todo_todo_item_notes_idx": {Schema: "merp", Table: "todo_todo_item", Name: "todo_todo_item_notes_idx", Columns: []*ir.IndexColumn{{Name: "notes"}}, Where: "(notes IS NOT NULL)"},
		},
	}
	sub := &ir.Table{
		Schema: "merp", Name: "todo_subtask",
		Columns: []*ir.Column{{Name: "id"}, {Name: "item"}, {Name: "label"}},
		Constraints: map[string]*ir.Constraint{
			"todo_subtask_pkey": {Schema: "merp", Table: "todo_subtask", Name: "todo_subtask_pkey", Type: ir.ConstraintTypePrimaryKey, Columns: cols("id")},
			"todo_subtask_item_fkey": {Schema: "merp", Table: "todo_subtask", Name: "todo_subtask_item_fkey", Type: ir.ConstraintTypeForeignKey,
				Columns: cols("item"), ReferencedSchema: "merp", ReferencedTable: "todo_todo_item", ReferencedColumns: cols("id")},
		},
		Indexes: map[string]*ir.Index{
			"todo_subtask_label_idx": {Schema: "merp", Table: "todo_subtask", Name: "todo_subtask_label_idx", Columns: []*ir.IndexColumn{{Name: "label"}}},
			"todo_subtask_pkey":      {Schema: "merp", Table: "todo_subtask", Name: "todo_subtask_pkey"},
		},
	}
	// A table that points at todo_subtask by foreign key and is not renamed.
	attach := &ir.Table{
		Schema: "merp", Name: "todo_attachment",
		Columns: []*ir.Column{{Name: "id"}, {Name: "subtask"}},
		Constraints: map[string]*ir.Constraint{
			"todo_attachment_subtask_fkey": {Schema: "merp", Table: "todo_attachment", Name: "todo_attachment_subtask_fkey", Type: ir.ConstraintTypeForeignKey,
				Columns: cols("subtask"), ReferencedSchema: "merp", ReferencedTable: "todo_subtask", ReferencedColumns: cols("id")},
		},
		Indexes: map[string]*ir.Index{},
	}
	return &ir.IR{Schemas: map[string]*ir.Schema{"merp": {Name: "merp", Tables: map[string]*ir.Table{
		item.Name: item, sub.Name: sub, attach.Name: attach,
	}}}}
}

func rename(kind, from, to string) Entry {
	return Entry{Op: OpRename, Kind: kind, From: from, To: to}
}

func sqls(t *testing.T, entries []Entry, in *ir.IR) ([]string, []Result) {
	t.Helper()
	diffs, results, err := Apply(in, "merp", entries)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var out []string
	for _, d := range diffs {
		for _, s := range d.Statements {
			out = append(out, s.SQL)
		}
	}
	return out, results
}

func TestApplyRenamesExactlyWhatTheBlockLists(t *testing.T) {
	in := current()
	got, results := sqls(t, []Entry{
		rename(KindTable, "merp.todo_subtask", "merp.todo_step"),
		rename(KindConstraint, "merp.todo_step.todo_subtask_pkey", "merp.todo_step.todo_step_pkey"),
		rename(KindIndex, "merp.todo_subtask_label_idx", "merp.todo_step_label_idx"),
		rename(KindColumn, "merp.todo_todo_item.notes", "merp.todo_todo_item.description"),
	}, in)

	want := []string{
		"ALTER TABLE merp.todo_subtask RENAME TO todo_step;",
		"ALTER TABLE merp.todo_step RENAME CONSTRAINT todo_subtask_pkey TO todo_step_pkey;",
		"ALTER INDEX merp.todo_subtask_label_idx RENAME TO todo_step_label_idx;",
		"ALTER TABLE merp.todo_todo_item RENAME COLUMN notes TO description;",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("statements in the block's order\n got: %q\nwant: %q", got, want)
	}
	for _, r := range results {
		if !r.Applied {
			t.Errorf("%s should have applied", r.Entry)
		}
	}

	s := in.Schemas["merp"]
	step := s.Tables["todo_step"]
	if s.Tables["todo_subtask"] != nil || step == nil || step.Name != "todo_step" {
		t.Fatalf("the table is not renamed in memory: %v", s.Tables)
	}
	// Listed: renamed. Not listed: kept, no cascade from the table to its names.
	if step.Constraints["todo_step_pkey"] == nil || step.Constraints["todo_step_pkey"].Name != "todo_step_pkey" {
		t.Error("the listed constraint is not renamed")
	}
	if step.Indexes["todo_step_pkey"] == nil {
		t.Error("the index behind a renamed primary key follows it, as PostgreSQL does")
	}
	if step.Constraints["todo_subtask_item_fkey"] == nil {
		t.Error("an unlisted constraint must keep its name (no cascade)")
	}
	if step.Indexes["todo_step_label_idx"] == nil || step.Indexes["todo_subtask_label_idx"] != nil {
		t.Error("the listed index is not renamed")
	}
	for _, c := range step.Constraints {
		if c.Table != "todo_step" {
			t.Errorf("constraint %s still names table %s", c.Name, c.Table)
		}
	}
	for _, i := range step.Indexes {
		if i.Table != "todo_step" {
			t.Errorf("index %s still names table %s", i.Name, i.Table)
		}
	}
	if got := s.Tables["todo_attachment"].Constraints["todo_attachment_subtask_fkey"].ReferencedTable; got != "todo_step" {
		t.Errorf("a foreign key into the renamed table must follow it, references %s", got)
	}
}

func TestApplyColumnRenameFollowsTheNamesPostgresRewrites(t *testing.T) {
	in := current()
	sqls(t, []Entry{rename(KindColumn, "merp.todo_todo_item.notes", "merp.todo_todo_item.description")}, in)
	item := in.Schemas["merp"].Tables["todo_todo_item"]
	if item.Columns[1].Name != "description" {
		t.Errorf("column not renamed: %s", item.Columns[1].Name)
	}
	if got := item.Constraints["todo_todo_item_notes_chk"].CheckClause; got != `(char_length(description) < 20 AND description <> 'notes')` {
		t.Errorf("check clause: identifier renamed, literal left alone; got %s", got)
	}
	idx := item.Indexes["todo_todo_item_notes_idx"]
	if idx.Columns[0].Name != "description" || idx.Where != "(description IS NOT NULL)" {
		t.Errorf("index not rewritten: %v %s", idx.Columns[0].Name, idx.Where)
	}
	if item.Constraints["todo_todo_item_notes_chk"].Name != "todo_todo_item_notes_chk" {
		t.Error("a constraint named after the column keeps its name unless listed")
	}
}

func TestApplyColumnRenameFollowsAForeignKeyReferencingIt(t *testing.T) {
	in := current()
	sqls(t, []Entry{rename(KindColumn, "merp.todo_subtask.id", "merp.todo_subtask.key")}, in)
	fk := in.Schemas["merp"].Tables["todo_attachment"].Constraints["todo_attachment_subtask_fkey"]
	if fk.ReferencedColumns[0].Name != "key" {
		t.Errorf("referenced column: %s", fk.ReferencedColumns[0].Name)
	}
}

func TestApplyOldAbsentDoesNothingAndIsReportedInert(t *testing.T) {
	in := current()
	got, results := sqls(t, []Entry{
		rename(KindTable, "merp.todo_gone", "merp.todo_there"),
		rename(KindColumn, "merp.todo_todo_item.gone", "merp.todo_todo_item.there"),
		rename(KindColumn, "merp.no_table.a", "merp.no_table.b"),
		rename(KindConstraint, "merp.todo_todo_item.gone", "merp.todo_todo_item.there"),
		rename(KindIndex, "merp.gone_idx", "merp.there_idx"),
	}, in)
	if len(got) != 0 {
		t.Errorf("no statement expected, got %q", got)
	}
	if len(results) != 5 {
		t.Fatalf("one result per entry, got %d", len(results))
	}
	for _, r := range results {
		if r.Applied {
			t.Errorf("%s must be inert", r.Entry)
		}
	}
}

func TestApplyAbsentOldWithPresentNewIsInertNotRefused(t *testing.T) {
	// A database already renamed holds the new name only.
	in := current()
	_, results := sqls(t, []Entry{rename(KindTable, "merp.todo_was", "merp.todo_subtask")}, in)
	if results[0].Applied {
		t.Error("old absent: nothing to do, whatever the new name")
	}
}

func TestApplyBothPresentIsRefusedByName(t *testing.T) {
	cases := []Entry{
		rename(KindTable, "merp.todo_subtask", "merp.todo_attachment"),
		rename(KindColumn, "merp.todo_subtask.item", "merp.todo_subtask.label"),
		rename(KindConstraint, "merp.todo_subtask.todo_subtask_item_fkey", "merp.todo_subtask.todo_subtask_pkey"),
		rename(KindIndex, "merp.todo_subtask_label_idx", "merp.todo_todo_item_notes_idx"),
		rename(KindIndex, "merp.todo_subtask_label_idx", "merp.todo_attachment"),
	}
	for _, e := range cases {
		t.Run(e.String(), func(t *testing.T) {
			_, _, err := Apply(current(), "merp", []Entry{e})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), "both exist") || !strings.Contains(err.Error(), e.From) || !strings.Contains(err.Error(), e.To) {
				t.Errorf("the refusal must name the entry and say both exist: %v", err)
			}
		})
	}
}

func TestApplyDoesNotInferAnythingFromShape(t *testing.T) {
	// A drop and an add of equal shape stay what the differ makes of them: an
	// empty block renames nothing, and the current state is left as inspected.
	in := current()
	got, results := sqls(t, nil, in)
	if len(got) != 0 || len(results) != 0 || in.Schemas["merp"].Tables["todo_subtask"] == nil {
		t.Error("an empty block must change nothing")
	}
}

func TestApplyEntryOrderDecidesWhichNameAConstraintPathUses(t *testing.T) {
	// Written before its table's rename, the constraint path names a table
	// that does not exist yet: inert, never reordered.
	in := current()
	_, results := sqls(t, []Entry{
		rename(KindConstraint, "merp.todo_step.todo_subtask_pkey", "merp.todo_step.todo_step_pkey"),
		rename(KindTable, "merp.todo_subtask", "merp.todo_step"),
	}, in)
	if results[0].Applied || !results[1].Applied {
		t.Errorf("applied: %v %v", results[0].Applied, results[1].Applied)
	}
}

func TestApplyRefusesAnotherSchema(t *testing.T) {
	_, _, err := Apply(current(), "merp", []Entry{rename(KindTable, "other.a", "other.b")})
	if err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Errorf("expected a refusal naming the schema, got %v", err)
	}
}

func TestValidateRefusesByName(t *testing.T) {
	cases := []struct {
		e    Entry
		want string
	}{
		{Entry{Op: "move", Kind: KindTable, From: "merp.a", To: "merp.b"}, `unsupported op "move"`},
		{Entry{Op: OpRename, Kind: "view", From: "merp.a", To: "merp.b"}, `unsupported kind "view"`},
		{rename(KindTable, "a", "b"), "schema-qualified"},
		{rename(KindColumn, "merp.t", "merp.t2"), "schema-qualified"},
		{rename(KindTable, "merp.a", "merp.a"), "equal"},
		{rename(KindColumn, "merp.t.a", "merp.u.a2"), "only the last name"},
		{rename(KindTable, "merp.a", "other.b"), "only the last name"},
		{rename(KindTable, "merp.", "merp.b"), "empty"},
	}
	for _, c := range cases {
		err := c.e.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want %q, got %v", c.e, c.want, err)
		}
	}
}

func TestReplaceIdent(t *testing.T) {
	cases := [][3]string{
		{`(notes IS NOT NULL)`, `notes`, `description`},
		{`notes_x = 1 AND x_notes = 2`, `notes`, `notes`},
		{`t.notes = 'it''s notes'`, `notes`, `description`},
		{`("notes" > 1)`, `notes`, `description`},
	}
	want := []string{`(description IS NOT NULL)`, `notes_x = 1 AND x_notes = 2`, `t.description = 'it''s notes'`, `("description" > 1)`}
	for i, c := range cases {
		if got := replaceIdent(c[0], c[1], c[2]); got != want[i] {
			t.Errorf("%s: got %s want %s", c[0], got, want[i])
		}
	}
}
