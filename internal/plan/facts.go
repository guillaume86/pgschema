package plan

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/ir"
)

// This file is patch 6 of the merp fork (see fork/README.md): every step of the
// in-transaction plan (patch 4) carries what its statement does as typed
// facts read off the diff that produced it, so a consumer never parses the
// step's SQL to tell a rewrite, a NOT NULL, a CHECK and its length cap, a
// unique group or a foreign key.

// Step operations a StepFacts names.
const (
	FactAddColumn     = "add_column"
	FactAlterType     = "alter_type"
	FactSetNotNull    = "set_not_null"
	FactAddConstraint = "add_constraint"
	FactCreateIndex   = "create_index"
)

// Constraint kinds a StepConstraint names. A unique index is kind unique, as a
// UNIQUE constraint is; its operation tells them apart.
const (
	KindNotNull    = "not_null"
	KindCheck      = "check"
	KindUnique     = "unique"
	KindPrimaryKey = "primary_key"
	KindForeignKey = "foreign_key"
	KindExclusion  = "exclusion"
	KindIndex      = "index"
)

// StepFacts is what one plan step does, as the differ knew it.
type StepFacts struct {
	Op         string          `json:"op"`
	Schema     string          `json:"schema"`
	Table      string          `json:"table"`
	Column     string          `json:"column,omitempty"`
	Rewrites   bool            `json:"rewrites,omitempty"` // the statement rewrites every row of its table
	Constraint *StepConstraint `json:"constraint,omitempty"`
}

// StepConstraint is the shape of the constraint (or index) a step adds over
// existing rows: what a consumer counts violations of before it runs.
type StepConstraint struct {
	Name             string         `json:"name,omitempty"` // empty for a NOT NULL, which the model names no constraint for
	Kind             string         `json:"kind"`
	Columns          []string       `json:"columns,omitempty"`
	Check            string         `json:"check,omitempty"`      // a CHECK's predicate, without the CHECK keyword
	MaxLength        *int           `json:"max_length,omitempty"` // when the CHECK is exactly char_length(column) <= n
	NullsNotDistinct bool           `json:"nulls_not_distinct,omitempty"`
	Where            string         `json:"where,omitempty"`      // a partial index's predicate
	Expression       bool           `json:"expression,omitempty"` // an index over expressions: Columns are absent
	References       *StepReference `json:"references,omitempty"`
}

// StepReference is a foreign key's target.
type StepReference struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// stepFacts reads what stmt, one of d's canonical statements, does. It is nil
// for a statement that adds no column, changes no type and adds no constraint
// or index.
func stepFacts(d diff.Diff, stmt string) *StepFacts {
	switch source := d.Source.(type) {
	case *ir.Column:
		if d.Type == diff.DiffTypeTableColumn && d.Operation == diff.DiffOperationCreate {
			schema, table := columnTable(d.Path, source.Name)
			return &StepFacts{Op: FactAddColumn, Schema: schema, Table: table, Column: source.Name, Rewrites: volatileDefault(source)}
		}
	case *diff.ColumnDiff:
		if d.Type == diff.DiffTypeTableColumn && d.Operation == diff.DiffOperationAlter {
			return columnFacts(d.Path, source, stmt)
		}
	case *ir.Constraint:
		if d.Type == diff.DiffTypeTableConstraint && d.Operation == diff.DiffOperationCreate {
			return &StepFacts{Op: FactAddConstraint, Schema: source.Schema, Table: source.Table, Constraint: constraintShape(source)}
		}
	case *ir.Index:
		creates := d.Operation == diff.DiffOperationCreate || strings.HasPrefix(strings.TrimSpace(stmt), "CREATE ")
		if (d.Type == diff.DiffTypeTableIndex || d.Type == diff.DiffTypeMaterializedViewIndex) && creates {
			return &StepFacts{Op: FactCreateIndex, Schema: source.Schema, Table: source.Table, Constraint: indexShape(source)}
		}
	}
	return nil
}

// columnFacts tells which of a column change's statements stmt is: the
// differ emits one diff per statement of a ColumnDiff, all with the same
// source, so the statement it generated is read back by its clause.
func columnFacts(path string, cd *diff.ColumnDiff, stmt string) *StepFacts {
	column := ir.QuoteIdentifier(cd.New.Name)
	schema, table := columnTable(path, cd.New.Name)
	facts := &StepFacts{Schema: schema, Table: table, Column: cd.New.Name}
	switch {
	case strings.Contains(stmt, "ALTER COLUMN "+column+" SET NOT NULL"):
		facts.Op = FactSetNotNull
		facts.Constraint = &StepConstraint{Kind: KindNotNull, Columns: []string{cd.New.Name}}
	case strings.Contains(stmt, "ALTER COLUMN "+column+" TYPE "):
		facts.Op = FactAlterType
		facts.Rewrites = true
	default:
		return nil
	}
	return facts
}

// columnTable splits a column diff's path, schema.table.column, knowing the
// column's name (which may hold a dot).
func columnTable(path, column string) (string, string) {
	qualified := strings.TrimSuffix(path, "."+column)
	schema, table, found := strings.Cut(qualified, ".")
	if !found {
		return "", qualified
	}
	return schema, table
}

func constraintShape(c *ir.Constraint) *StepConstraint {
	shape := &StepConstraint{Name: c.Name, Columns: constraintColumns(c.Columns), NullsNotDistinct: c.NullsNotDistinct}
	switch c.Type {
	case ir.ConstraintTypeCheck:
		shape.Kind = KindCheck
		shape.Check = checkPredicate(c.CheckClause)
		shape.MaxLength = maxLength(shape.Check, shape.Columns)
	case ir.ConstraintTypeUnique:
		shape.Kind = KindUnique
	case ir.ConstraintTypePrimaryKey:
		shape.Kind = KindPrimaryKey
	case ir.ConstraintTypeForeignKey:
		shape.Kind = KindForeignKey
		shape.References = &StepReference{Schema: c.ReferencedSchema, Table: c.ReferencedTable, Columns: constraintColumns(c.ReferencedColumns)}
		if shape.References.Schema == "" {
			shape.References.Schema = c.Schema
		}
	case ir.ConstraintTypeExclusion:
		shape.Kind = KindExclusion
	}
	return shape
}

func indexShape(index *ir.Index) *StepConstraint {
	shape := &StepConstraint{Name: index.Name, Kind: KindIndex, NullsNotDistinct: index.NullsNotDistinct, Where: index.Where, Expression: index.IsExpression}
	if index.Type == ir.IndexTypeUnique || index.Type == ir.IndexTypePrimary {
		shape.Kind = KindUnique
	}
	if !index.IsExpression {
		for _, column := range index.Columns {
			shape.Columns = append(shape.Columns, unquote(column.Name))
		}
	}
	return shape
}

// constraintColumns lists a constraint's columns in their declared order.
func constraintColumns(columns []*ir.ConstraintColumn) []string {
	ordered := make([]*ir.ConstraintColumn, len(columns))
	copy(ordered, columns)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	names := make([]string, 0, len(ordered))
	for _, column := range ordered {
		names = append(names, column.Name)
	}
	return names
}

// checkPredicate is a CHECK clause without its keyword: CHECK (x > 0) is (x > 0).
func checkPredicate(clause string) string {
	clause = strings.TrimSpace(clause)
	if len(clause) > 5 && strings.EqualFold(clause[:5], "CHECK") {
		return strings.TrimSpace(clause[5:])
	}
	return clause
}

var lengthCap = regexp.MustCompile(`^\(*\s*char_length\(\s*("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)\s*\)\s*<=\s*(\d+)\s*\)*$`)

// maxLength is n when predicate is exactly char_length(column) <= n over the
// constraint's one column, and nil for any other CHECK.
func maxLength(predicate string, columns []string) *int {
	if len(columns) != 1 {
		return nil
	}
	match := lengthCap.FindStringSubmatch(predicate)
	if match == nil || unquote(match[1]) != columns[0] || strings.Count(predicate, "(") != strings.Count(predicate, ")") {
		return nil
	}
	n, err := strconv.Atoi(match[2])
	if err != nil {
		return nil
	}
	return &n
}

func unquote(name string) string {
	if len(name) > 1 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

var volatileCall = regexp.MustCompile(`(?i)\b(gen_random_uuid|uuid_generate_v4|random|clock_timestamp|timeofday|nextval)\s*\(`)

// volatileDefault is true for a column added with a default PostgreSQL
// evaluates per row, which writes every row of the table.
func volatileDefault(column *ir.Column) bool {
	return column.DefaultValue != nil && volatileCall.MatchString(*column.DefaultValue)
}
