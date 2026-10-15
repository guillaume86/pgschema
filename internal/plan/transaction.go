package plan

import (
	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/internal/fingerprint"
	"github.com/pgplex/pgschema/ir"
)

// This file is patch 4 of the merp fork (see fork/README.md): the plan in the
// form that runs inside one caller-owned transaction.

// NewPlanInTransaction is NewPlanWithFingerprint for a plan the caller runs
// inside one transaction: every statement is the differ's canonical one (a
// plain CREATE INDEX, ADD CONSTRAINT and SET NOT NULL, no CONCURRENTLY, no NOT
// VALID then VALIDATE), no step carries a wait directive, and the steps form
// one execution group. generateRewrite is never consulted. A nil
// sourceFingerprint leaves the plan without one.
func NewPlanInTransaction(diffs []diff.Diff, sourceFingerprint *fingerprint.SchemaFingerprint, targetMajorVersion int, currentIR *ir.IR) *Plan {
	p := NewPlanWithFingerprint(diffs, sourceFingerprint, targetMajorVersion, currentIR)
	p.Groups = groupInOneTransaction(diffs)
	return p
}

// groupInOneTransaction puts the canonical statements of every diff, in order,
// into a single execution group (nil when there are none).
func groupInOneTransaction(diffs []diff.Diff) []ExecutionGroup {
	var steps []Step
	for _, d := range diffs {
		for _, stmt := range d.Statements {
			steps = append(steps, Step{
				SQL:       stmt.SQL,
				Type:      d.Type.String(),
				Operation: d.Operation.String(),
				Path:      d.Path,
			})
		}
	}
	if len(steps) == 0 {
		return nil
	}
	return []ExecutionGroup{{Steps: steps}}
}
