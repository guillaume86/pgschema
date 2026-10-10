package plan

import (
	"fmt"
	"strings"

	"github.com/pgplex/pgschema/cmd/util"
	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/internal/fingerprint"
	"github.com/pgplex/pgschema/internal/plan"
	"github.com/pgplex/pgschema/ir"
)

// desiredIRSource is a desired-state provider that already holds the desired
// IR: the merp fork's .pgmodel provider (internal/model). GeneratePlan hands
// it to generatePlanFromIR instead of applying SQL to it.
type desiredIRSource interface {
	DesiredIR(schema string) (*ir.IR, error)
}

// generatePlanFromIR is GeneratePlan for a desired state given as IR. It keeps
// GeneratePlan's current-state half (ignore config, inspection, source
// fingerprint for apply --plan's drift check, target version, diff) and
// replaces the SQL half (include processing, stubs, role validation, apply to
// the plan database, inspection) with the provider's IR. Keep it in step with
// GeneratePlan on every rebase (fork/README.md).
func generatePlanFromIR(config *PlanConfig, source desiredIRSource) (*plan.Plan, error) {
	ignoreConfig, err := util.LoadIgnoreFileWithStructure()
	if err != nil {
		return nil, fmt.Errorf("failed to load .pgschemaignore: %w", err)
	}

	desiredStateIR, err := source.DesiredIR(config.Schema)
	if err != nil {
		return nil, err
	}

	currentStateIR, err := util.GetIRFromDatabase(config.Host, config.Port, config.DB, config.User, config.Password, config.SSLMode, config.Schema, config.ApplicationName, ignoreConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get current state from database: %w", err)
	}

	sourceFingerprint, err := fingerprint.ComputeFingerprint(currentStateIR, config.Schema)
	if err != nil {
		return nil, fmt.Errorf("failed to compute source fingerprint: %w", err)
	}

	targetMajorVersion := 0
	if v, ok := strings.CutPrefix(currentStateIR.Metadata.DatabaseVersion, "PostgreSQL "); ok {
		fmt.Sscanf(v, "%d", &targetMajorVersion)
	}

	diffs := diff.GenerateMigrationForTarget(currentStateIR, desiredStateIR, config.Schema, targetMajorVersion)
	return plan.NewPlanWithFingerprint(diffs, sourceFingerprint, targetMajorVersion, currentStateIR), nil
}
