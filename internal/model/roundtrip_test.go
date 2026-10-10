package model_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgplex/pgschema/cmd/apply"
	planCmd "github.com/pgplex/pgschema/cmd/plan"
	"github.com/pgplex/pgschema/internal/model"
	"github.com/pgplex/pgschema/internal/plan"
	"github.com/pgplex/pgschema/ir"
	"github.com/pgplex/pgschema/testutil"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// roundTripFixtures are upstream diff fixtures (testdata/diff/<case>) whose
// new.sql becomes a .pgmodel: a serial column (derived IsSerial), composite
// keys, a CHECK, and a partial index (normalized WHERE clause).
var roundTripFixtures = []string{
	"create_table/add_column_serial",
	"create_table/add_table_composite_keys",
	"create_table/add_check",
	"create_index/issue_473_partial_index_in_list",
}

// TestModelRoundTrip materializes a fixture's new.sql the upstream way
// (applied to Postgres, inspected into an IR), writes that IR as a .pgmodel,
// and checks against a real target:
//   - planning the model against a target holding new.sql is empty;
//   - against a target holding old.sql, plan(model) equals plan(file).
func TestModelRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	target := testutil.SetupPostgres(t)
	defer target.Stop()
	planDB := testutil.SetupPostgres(t)
	defer planDB.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, target)
	defer conn.Close()

	for _, fixture := range roundTripFixtures {
		t.Run(strings.ReplaceAll(fixture, "/", "_"), func(t *testing.T) {
			dir := filepath.Join("..", "..", "testdata", "diff", fixture)
			modelPath := filepath.Join(t.TempDir(), "desired.pgmodel")

			materialize(t, conn, "public", readFile(t, filepath.Join(dir, "new.sql")))
			writeModel(t, conn, "public", modelPath)

			modelConfig := &planCmd.PlanConfig{Host: host, Port: port, DB: db, User: user, Password: password,
				Schema: "public", File: modelPath, ApplicationName: "pgschema", SSLMode: "disable"}
			provider, err := planCmd.CreateDesiredStateProvider(modelConfig)
			if err != nil {
				t.Fatalf("CreateDesiredStateProvider: %v", err)
			}

			converged, err := planCmd.GeneratePlan(modelConfig, provider)
			if err != nil {
				t.Fatalf("plan(model) against new.sql: %v", err)
			}
			if converged.HasAnyChanges() {
				t.Errorf("plan(model) against the state it was taken from should be empty, got:\n%s", converged.ToSQL(plan.SQLFormatRaw))
			}

			materialize(t, conn, "public", readFile(t, filepath.Join(dir, "old.sql")))
			fileConfig := *modelConfig
			fileConfig.File = filepath.Join(dir, "new.sql")
			fromFile, err := planCmd.GeneratePlan(&fileConfig, planDB)
			if err != nil {
				t.Fatalf("plan(file) against old.sql: %v", err)
			}
			fromModel, err := planCmd.GeneratePlan(modelConfig, provider)
			if err != nil {
				t.Fatalf("plan(model) against old.sql: %v", err)
			}
			if got, want := fromModel.ToSQL(plan.SQLFormatRaw), fromFile.ToSQL(plan.SQLFormatRaw); got != want {
				t.Errorf("plan(model) differs from plan(file)\n--- file ---\n%s\n--- model ---\n%s", want, got)
			}
		})
	}
}

// TestModelFilePlanAndApply drives the CLI with --file x.pgmodel: plan and
// apply against an empty "merp" schema (the shape erp uses), apply --plan's
// drift check on a plan made from a model, and convergence after apply.
func TestModelFilePlanAndApply(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	materialize(t, conn, "merp", `
		CREATE TABLE tenant (id uuid PRIMARY KEY, name text NOT NULL UNIQUE);
		CREATE TABLE item (
			id uuid PRIMARY KEY,
			tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
			qty numeric(12,3) NOT NULL DEFAULT 0 CHECK (qty >= 0),
			status text NOT NULL DEFAULT 'draft',
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX item_tenant_status_idx ON item (tenant_id, status) WHERE status <> 'archived';`)
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "app.pgmodel")
	writeModel(t, conn, "merp", modelPath)
	materialize(t, conn, "merp", "")

	connArgs := []string{"--host", host, "--port", fmt.Sprint(port), "--db", db, "--user", user, "--password", password, "--sslmode", "disable", "--schema", "merp"}

	firstPlan := filepath.Join(dir, "first.json")
	run(t, planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", firstPlan)...)
	if p := readPlan(t, firstPlan); !p.HasAnyChanges() {
		t.Fatalf("plan --file x.pgmodel against an empty schema should create the tables")
	}

	// A plan made from a model carries the target's fingerprint, so apply --plan refuses a drifted target.
	exec(t, conn, "CREATE TABLE merp.stray (id int)")
	if err := execute(apply.ApplyCmd, append(connArgs, "--plan", firstPlan, "--auto-approve")...); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("apply --plan should refuse a drifted target, got %v", err)
	}
	exec(t, conn, "DROP TABLE merp.stray")

	run(t, apply.ApplyCmd, append(connArgs, "--file", modelPath, "--auto-approve")...)

	secondPlan := filepath.Join(dir, "second.json")
	run(t, planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", secondPlan)...)
	if p := readPlan(t, secondPlan); p.HasAnyChanges() {
		t.Errorf("plan after apply should be empty, got:\n%s", p.ToSQL(plan.SQLFormatRaw))
	}
}

func run(t *testing.T, cmd *cobra.Command, args ...string) {
	t.Helper()
	if err := execute(cmd, args...); err != nil {
		t.Fatalf("%s %s: %v", cmd.Name(), strings.Join(args, " "), err)
	}
}

// execute runs a command with fresh flags: cobra keeps flag values and their
// Changed state between Execute calls, which would trip the exclusive groups.
func execute(cmd *cobra.Command, args ...string) error {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	cmd.SetArgs(args)
	return cmd.Execute()
}

// materialize recreates schema and runs sqlText in it. search_path is set in
// the same Exec, since the pool may hand the next Exec another connection.
func materialize(t *testing.T, conn *sql.DB, schema, sqlText string) {
	t.Helper()
	q := ir.QuoteIdentifier(schema)
	exec(t, conn, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE; CREATE SCHEMA %s; SET search_path TO %s;\n%s", q, q, q, sqlText))
}

func exec(t *testing.T, conn *sql.DB, sqlText string) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), sqlText); err != nil {
		t.Fatalf("exec %q: %v", firstLine(sqlText), err)
	}
}

// writeModel inspects schema and writes it as a .pgmodel document at path.
func writeModel(t *testing.T, conn *sql.DB, schema, path string) {
	t.Helper()
	inspected, err := ir.NewInspector(conn, nil).BuildIR(context.Background(), schema)
	if err != nil {
		t.Fatalf("inspect %s: %v", schema, err)
	}
	data, err := json.MarshalIndent(map[string]any{"specVersion": model.SpecVersion, "model": inspected}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPlan(t *testing.T, path string) *plan.Plan {
	t.Helper()
	p, err := plan.FromJSON([]byte(readFile(t, path)))
	if err != nil {
		t.Fatalf("read plan %s: %v", path, err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
