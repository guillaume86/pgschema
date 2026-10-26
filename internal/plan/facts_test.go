package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/ir"
)

// merp fork, patch 6 (facts.go): the in-transaction plan's steps carry typed
// facts read off the diff, never off the SQL by the consumer. Each test diffs
// two IRs of one table in schema merp through the real differ.

func factsTable(columns []*ir.Column, constraints []*ir.Constraint, indexes []*ir.Index) *ir.IR {
	table := &ir.Table{
		Schema: "merp", Name: "task", Type: ir.TableTypeBase, Columns: columns,
		Constraints: map[string]*ir.Constraint{}, Indexes: map[string]*ir.Index{},
		Triggers: map[string]*ir.Trigger{}, Policies: map[string]*ir.RLSPolicy{},
	}
	for _, c := range constraints {
		table.Constraints[c.Name] = c
	}
	for _, index := range indexes {
		table.Indexes[index.Name] = index
	}
	state := ir.NewIR()
	state.Schemas["merp"] = &ir.Schema{Name: "merp", Tables: map[string]*ir.Table{"task": table}}
	return state
}

func factsColumns(extra ...*ir.Column) []*ir.Column {
	return append([]*ir.Column{
		{Name: "id", Position: 1, DataType: "uuid"},
		{Name: "title", Position: 2, DataType: "text", IsNullable: true},
		{Name: "parent", Position: 3, DataType: "uuid", IsNullable: true},
	}, extra...)
}

func idKey() *ir.Constraint {
	return &ir.Constraint{Schema: "merp", Table: "task", Name: "task_pkey", Type: ir.ConstraintTypePrimaryKey, Columns: []*ir.ConstraintColumn{{Name: "id", Position: 1}}}
}

// inTransactionSteps plans old to new in the in-transaction form and returns
// every step, facts or not.
func inTransactionSteps(t *testing.T, old, new *ir.IR) []Step {
	t.Helper()
	p := NewPlanInTransaction(diff.GenerateMigration(old, new, "merp"), nil, 18, old)
	if len(p.Groups) != 1 {
		t.Fatalf("expected one group, got %d", len(p.Groups))
	}
	return p.Groups[0].Steps
}

func onlyFacts(t *testing.T, steps []Step) *StepFacts {
	t.Helper()
	var found []*StepFacts
	for _, step := range steps {
		if step.Facts != nil {
			found = append(found, step.Facts)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one step with facts, got %d: %+v", len(found), steps)
	}
	return found[0]
}

func intRef(n int) *int { return &n }

func strRef(s string) *string { return &s }

func TestFactsSetNotNullNamesTheColumn(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	required := factsColumns()
	required[1].IsNullable = false
	got := onlyFacts(t, inTransactionSteps(t, old, factsTable(required, []*ir.Constraint{idKey()}, nil)))

	want := &StepFacts{Op: FactSetNotNull, Schema: "merp", Table: "task", Column: "title", Constraint: &StepConstraint{Kind: KindNotNull, Columns: []string{"title"}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
}

func TestFactsTypeChangeRewritesAndItsNotNullIsItsOwnStep(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	changed := factsColumns()
	changed[1].DataType = "integer"
	changed[1].IsNullable = false
	steps := inTransactionSteps(t, old, factsTable(changed, []*ir.Constraint{idKey()}, nil))

	var ops []string
	for _, step := range steps {
		if step.Facts != nil {
			ops = append(ops, step.Facts.Op)
			if step.Facts.Rewrites != (step.Facts.Op == FactAlterType) {
				t.Fatalf("rewrites %v on %s: %s", step.Facts.Rewrites, step.Facts.Op, step.SQL)
			}
		}
	}
	if diff := cmp.Diff([]string{FactAlterType, FactSetNotNull}, ops); diff != "" {
		t.Fatalf("ops (-want +got):\n%s", diff)
	}
}

func TestFactsColumnAddedWithAVolatileDefaultRewrites(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	added := factsTable(factsColumns(&ir.Column{Name: "token", Position: 4, DataType: "uuid", IsNullable: true, DefaultValue: strRef("gen_random_uuid()")}), []*ir.Constraint{idKey()}, nil)
	got := onlyFacts(t, inTransactionSteps(t, old, added))

	want := &StepFacts{Op: FactAddColumn, Schema: "merp", Table: "task", Column: "token", Rewrites: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
}

func TestFactsColumnAddedWithAConstantDefaultDoesNotRewrite(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	added := factsTable(factsColumns(&ir.Column{Name: "rank", Position: 4, DataType: "integer", IsNullable: true, DefaultValue: strRef("3")}), []*ir.Constraint{idKey()}, nil)

	if got := onlyFacts(t, inTransactionSteps(t, old, added)); got.Rewrites {
		t.Fatalf("a constant default rewrites: %+v", got)
	}
}

func TestFactsLengthCheckCarriesItsCap(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	check := &ir.Constraint{Schema: "merp", Table: "task", Name: "task_title_length", Type: ir.ConstraintTypeCheck, Columns: []*ir.ConstraintColumn{{Name: "title", Position: 1}}, CheckClause: "CHECK (char_length(title) <= 20)"}
	got := onlyFacts(t, inTransactionSteps(t, old, factsTable(factsColumns(), []*ir.Constraint{idKey(), check}, nil)))

	want := &StepFacts{Op: FactAddConstraint, Schema: "merp", Table: "task", Constraint: &StepConstraint{
		Name: "task_title_length", Kind: KindCheck, Columns: []string{"title"}, Check: "(char_length(title) <= 20)", MaxLength: intRef(20)}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
}

func TestFactsOtherCheckHasNoCap(t *testing.T) {
	for _, clause := range []string{"CHECK (char_length(title) <= 20 OR title = 'x')", "CHECK (char_length(parent::text) <= 20)", "CHECK (title <> '')"} {
		check := &ir.Constraint{Schema: "merp", Table: "task", Name: "task_title_check", Type: ir.ConstraintTypeCheck, Columns: []*ir.ConstraintColumn{{Name: "title", Position: 1}}, CheckClause: clause}
		if shape := constraintShape(check); shape.MaxLength != nil || shape.Kind != KindCheck {
			t.Fatalf("%s read as %+v", clause, shape)
		}
	}
}

func TestFactsUniqueConstraintListsItsColumnsInOrder(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	unique := &ir.Constraint{Schema: "merp", Table: "task", Name: "task_parent_title_key", Type: ir.ConstraintTypeUnique, NullsNotDistinct: true,
		Columns: []*ir.ConstraintColumn{{Name: "title", Position: 2}, {Name: "parent", Position: 1}}}
	got := onlyFacts(t, inTransactionSteps(t, old, factsTable(factsColumns(), []*ir.Constraint{idKey(), unique}, nil)))

	want := &StepFacts{Op: FactAddConstraint, Schema: "merp", Table: "task", Constraint: &StepConstraint{
		Name: "task_parent_title_key", Kind: KindUnique, Columns: []string{"parent", "title"}, NullsNotDistinct: true}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
}

func TestFactsForeignKeyNamesItsTarget(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	fk := &ir.Constraint{Schema: "merp", Table: "task", Name: "task_parent_fkey", Type: ir.ConstraintTypeForeignKey,
		Columns: []*ir.ConstraintColumn{{Name: "parent", Position: 1}}, ReferencedSchema: "merp", ReferencedTable: "task",
		ReferencedColumns: []*ir.ConstraintColumn{{Name: "id", Position: 1}}, DeleteRule: "NO ACTION", UpdateRule: "NO ACTION"}
	got := onlyFacts(t, inTransactionSteps(t, old, factsTable(factsColumns(), []*ir.Constraint{idKey(), fk}, nil)))

	want := &StepFacts{Op: FactAddConstraint, Schema: "merp", Table: "task", Constraint: &StepConstraint{
		Name: "task_parent_fkey", Kind: KindForeignKey, Columns: []string{"parent"},
		References: &StepReference{Schema: "merp", Table: "task", Columns: []string{"id"}}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
}

func TestFactsPartialUniqueIndexCarriesItsPredicateAndItsCommentNone(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	index := &ir.Index{Schema: "merp", Table: "task", Name: "task_title_idx", Type: ir.IndexTypeUnique, Method: "btree",
		Columns: []*ir.IndexColumn{{Name: "title", Position: 1}}, IsPartial: true, Where: "(parent IS NOT NULL)", Comment: "one title per parented task"}
	steps := inTransactionSteps(t, old, factsTable(factsColumns(), []*ir.Constraint{idKey()}, []*ir.Index{index}))
	got := onlyFacts(t, steps)

	want := &StepFacts{Op: FactCreateIndex, Schema: "merp", Table: "task", Constraint: &StepConstraint{
		Name: "task_title_idx", Kind: KindUnique, Columns: []string{"title"}, Where: "(parent IS NOT NULL)"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("facts (-want +got):\n%s", diff)
	}
	if len(steps) != 2 {
		t.Fatalf("expected the index and its comment, got %+v", steps)
	}
}

func TestFactsAreInThePlanJSONOfTheInTransactionPlanOnly(t *testing.T) {
	old := factsTable(factsColumns(), []*ir.Constraint{idKey()}, nil)
	required := factsColumns()
	required[1].IsNullable = false
	diffs := diff.GenerateMigration(old, factsTable(required, []*ir.Constraint{idKey()}, nil), "merp")

	inTransaction, err := NewPlanInTransaction(diffs, nil, 18, old).ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Groups []struct {
			Steps []struct {
				Facts map[string]any `json:"facts"`
			} `json:"steps"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(inTransaction), &read); err != nil {
		t.Fatal(err)
	}
	facts := read.Groups[0].Steps[0].Facts
	if facts["op"] != "set_not_null" || facts["column"] != "title" || facts["constraint"].(map[string]any)["kind"] != "not_null" {
		t.Fatalf("plan JSON facts: %s", inTransaction)
	}

	upstream, err := NewPlanWithFingerprint(diffs, nil, 18, old).ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(upstream, `"facts"`) {
		t.Fatalf("the upstream plan carries facts: %s", upstream)
	}
}
