package plan

import (
	"fmt"
	"strings"

	"github.com/pgplex/pgschema/cmd/util"
	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/internal/fingerprint"
	"github.com/pgplex/pgschema/internal/model"
	"github.com/pgplex/pgschema/internal/plan"
	"github.com/pgplex/pgschema/ir"
	"github.com/spf13/cobra"
)

// The merp fork's patch 4 (fork/README.md): --source, a current state read from
// a .pgmodel instead of the database, and --in-transaction, a plan whose
// statements run inside one transaction.

var (
	planSource        string
	planInTransaction bool
)

// registerMigrateFlags adds the patch's two flags to the plan command.
func registerMigrateFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&planSource, "source", "", "Path to a .pgmodel document read as the current state instead of the database (no connection needed, no fingerprint)")
	cmd.Flags().BoolVar(&planInTransaction, "in-transaction", false, "Plan every step as a plain statement for one surrounding transaction: no CONCURRENTLY, no wait directive, one execution group")
}

// planPreRunE is the connection check of upstream's plan command, skipped when
// --source names the current state: no database is read then.
func planPreRunE(dbPtr, userPtr, hostPtr *string, portPtr *int) func(*cobra.Command, []string) error {
	check := util.PreRunEWithEnvVarsAndConnection(dbPtr, userPtr, hostPtr, portPtr)
	return func(cmd *cobra.Command, args []string) error {
		if (planSource != "" || planInTransaction) && !model.IsDocumentPath(planFile) {
			return fmt.Errorf("--source and --in-transaction take a %s document for --file", model.Extension)
		}
		if planSource != "" {
			return nil
		}
		return check(cmd, args)
	}
}

// currentFromSource reads the --source document as the current state: the
// document's schema, normalized as an inspected one is, and the target major
// version from its metadata.databaseVersion.
func currentFromSource(path, schema string) (*ir.IR, int, error) {
	p, err := model.NewProvider(path)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read --source: %w", err)
	}
	current, err := p.DesiredIR(schema)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read --source: %w", err)
	}
	return current, majorVersionOf(current), nil
}

// majorVersionOf reads the PostgreSQL major version from "PostgreSQL 18.1"
// style metadata; 0 when unknown.
func majorVersionOf(i *ir.IR) int {
	major := 0
	if v, ok := strings.CutPrefix(i.Metadata.DatabaseVersion, "PostgreSQL "); ok {
		fmt.Sscanf(v, "%d", &major)
	}
	return major
}

// newPlan builds the plan in the form the configuration asks for.
func newPlan(config *PlanConfig, diffs []diff.Diff, fp *fingerprint.SchemaFingerprint, major int, current *ir.IR) *plan.Plan {
	if config.InTransaction {
		return plan.NewPlanInTransaction(diffs, fp, major, current)
	}
	return plan.NewPlanWithFingerprint(diffs, fp, major, current)
}
