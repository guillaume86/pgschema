package plan

import (
	"encoding/json"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/internal/refactor"
	"github.com/pgplex/pgschema/ir"
)

// refactorSource is a desired-state source that carries rename directives
// (the merp fork's .pgmodel provider).
type refactorSource interface {
	Refactor() []refactor.Entry
}

// applyRefactor renames the current-state IR in memory for the source's
// directives and returns the rename statements to run first in the plan, with
// the plan JSON's "refactor" report (nil when the source lists none).
func applyRefactor(source desiredIRSource, current *ir.IR, schema string) ([]diff.Diff, json.RawMessage, error) {
	rs, ok := source.(refactorSource)
	if !ok || len(rs.Refactor()) == 0 {
		return nil, nil, nil
	}
	diffs, results, err := refactor.Apply(current, schema, rs.Refactor())
	if err != nil {
		return nil, nil, err
	}
	report, err := json.Marshal(results)
	if err != nil {
		return nil, nil, err
	}
	return diffs, report, nil
}
