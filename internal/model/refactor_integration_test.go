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
	"github.com/pgplex/pgschema/ir"
	"github.com/pgplex/pgschema/testutil"
)

// The v2 release of a todo module and the v3 that renames its subtask entity
// to step and one field, as merp's Derive.Schema hands them to the engine.
const (
	renamesV2 = `
		CREATE TABLE todo_todo_item (id uuid PRIMARY KEY, title text NOT NULL, notes text);
		CREATE TABLE todo_subtask (
			id uuid NOT NULL, item uuid NOT NULL, label text NOT NULL,
			CONSTRAINT todo_subtask_pkey PRIMARY KEY (id),
			CONSTRAINT todo_subtask_item_fkey FOREIGN KEY (item) REFERENCES todo_todo_item(id));
		CREATE INDEX todo_subtask_label_idx ON todo_subtask (label);
		INSERT INTO todo_todo_item VALUES ('00000000-0000-0000-0000-000000000001', 'a', 'kept note');
		INSERT INTO todo_subtask VALUES ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', 'kept label');`
	renamesV3 = `
		CREATE TABLE todo_todo_item (id uuid PRIMARY KEY, title text NOT NULL, description text);
		CREATE TABLE todo_step (
			id uuid NOT NULL, item uuid NOT NULL, label text NOT NULL,
			CONSTRAINT todo_step_pkey PRIMARY KEY (id),
			CONSTRAINT todo_step_item_fkey FOREIGN KEY (item) REFERENCES todo_todo_item(id));
		CREATE INDEX todo_step_label_idx ON todo_step (label);`
)

var renamesBlock = []map[string]string{
	{"op": "rename", "kind": "table", "from": "merp.todo_subtask", "to": "merp.todo_step"},
	{"op": "rename", "kind": "constraint", "from": "merp.todo_step.todo_subtask_pkey", "to": "merp.todo_step.todo_step_pkey"},
	{"op": "rename", "kind": "constraint", "from": "merp.todo_step.todo_subtask_item_fkey", "to": "merp.todo_step.todo_step_item_fkey"},
	{"op": "rename", "kind": "index", "from": "merp.todo_subtask_label_idx", "to": "merp.todo_step_label_idx"},
	{"op": "rename", "kind": "column", "from": "merp.todo_todo_item.notes", "to": "merp.todo_todo_item.description"},
}

type refactorEntry struct {
	Op, Kind, From, To string
	Applied            bool
}

// TestRefactorRenamesByInspection drives plan and apply with a .pgmodel whose
// refactor block renames: the old database is renamed with its data, a second
// run finds nothing at the old names and plans nothing, a database holding both
// names is refused, and a fresh database creates v3 with every entry inert.
func TestRefactorRenamesByInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	dir := t.TempDir()
	modelPath := filepath.Join(dir, "v3.pgmodel")
	materialize(t, conn, "merp", renamesV3)
	writeRefactorModel(t, conn, "merp", modelPath, renamesBlock)
	connArgs := []string{"--host", host, "--port", fmt.Sprint(port), "--db", db, "--user", user, "--password", password, "--sslmode", "disable", "--schema", "merp"}
	planJSON := func(name string) (string, map[string]json.RawMessage, error) {
		out := filepath.Join(dir, name)
		err := execute(planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", out)...)
		if err != nil {
			return "", nil, err
		}
		raw := readFile(t, out)
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			t.Fatal(err)
		}
		return out, top, nil
	}
	entries := func(top map[string]json.RawMessage) []refactorEntry {
		var out []refactorEntry
		if err := json.Unmarshal(top["refactor"], &out); err != nil {
			t.Fatalf("refactor report: %v in %s", err, top["refactor"])
		}
		return out
	}

	t.Run("old names present: renamed with the data, nothing dropped", func(t *testing.T) {
		materialize(t, conn, "merp", renamesV2)
		path, top, err := planJSON("rename.json")
		if err != nil {
			t.Fatal(err)
		}
		p := readPlan(t, path)
		sql := p.ToSQL("raw")
		for _, want := range []string{
			"ALTER TABLE merp.todo_subtask RENAME TO todo_step;",
			"ALTER TABLE merp.todo_step RENAME CONSTRAINT todo_subtask_pkey TO todo_step_pkey;",
			"ALTER TABLE merp.todo_step RENAME CONSTRAINT todo_subtask_item_fkey TO todo_step_item_fkey;",
			"ALTER INDEX merp.todo_subtask_label_idx RENAME TO todo_step_label_idx;",
			"ALTER TABLE merp.todo_todo_item RENAME COLUMN notes TO description;",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("plan lacks %q:\n%s", want, sql)
			}
		}
		if strings.Contains(strings.ToUpper(sql), "DROP") {
			t.Errorf("a rename is not a drop and an add:\n%s", sql)
		}
		if first := p.Groups[0].Steps[0].SQL; !strings.Contains(first, "RENAME TO todo_step") {
			t.Errorf("the table rename runs first, got %q", first)
		}
		report := entries(top)
		if len(report) != len(renamesBlock) {
			t.Fatalf("one report entry per directive: %+v", report)
		}
		for i, r := range report {
			if !r.Applied || r.From != renamesBlock[i]["from"] || r.Kind != renamesBlock[i]["kind"] {
				t.Errorf("entry %d: %+v", i, r)
			}
		}

		run(t, apply.ApplyCmd, append(connArgs, "--file", modelPath, "--auto-approve")...)
		var note, label string
		if err := conn.QueryRow("SELECT description FROM merp.todo_todo_item").Scan(&note); err != nil || note != "kept note" {
			t.Errorf("the renamed column keeps its values: %q %v", note, err)
		}
		if err := conn.QueryRow("SELECT label FROM merp.todo_step").Scan(&label); err != nil || label != "kept label" {
			t.Errorf("the renamed table keeps its rows: %q %v", label, err)
		}
	})

	t.Run("second run: old names absent, nothing planned, every entry inert", func(t *testing.T) {
		path, top, err := planJSON("again.json")
		if err != nil {
			t.Fatal(err)
		}
		if readPlan(t, path).HasAnyChanges() {
			t.Error("a database already renamed plans nothing")
		}
		for _, r := range entries(top) {
			if r.Applied {
				t.Errorf("%+v must be inert", r)
			}
		}
	})

	t.Run("fresh database: neither name present, the tables are created, entries inert", func(t *testing.T) {
		materialize(t, conn, "merp", "")
		path, top, err := planJSON("fresh.json")
		if err != nil {
			t.Fatal(err)
		}
		if sql := readPlan(t, path).ToSQL("raw"); strings.Contains(sql, "RENAME") || !strings.Contains(sql, "CREATE TABLE") {
			t.Errorf("a fresh database creates:\n%s", sql)
		}
		for _, r := range entries(top) {
			if r.Applied {
				t.Errorf("%+v must be inert", r)
			}
		}
	})

	t.Run("both names present: refused naming the entry, nothing planned", func(t *testing.T) {
		materialize(t, conn, "merp", renamesV2+"\nCREATE TABLE todo_step (id int);")
		out := filepath.Join(dir, "both.json")
		err := execute(planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", out)...)
		if err == nil || !strings.Contains(err.Error(), "rename table merp.todo_subtask to merp.todo_step: both exist") {
			t.Fatalf("expected the both-present refusal, got %v", err)
		}
		if _, statErr := os.Stat(out); statErr == nil {
			t.Error("no plan is written")
		}
	})
}

// writeRefactorModel writes the inspected schema as a .pgmodel with a refactor block.
func writeRefactorModel(t *testing.T, conn *sql.DB, schema, path string, block []map[string]string) {
	t.Helper()
	inspected, err := ir.NewInspector(conn, nil).BuildIR(context.Background(), schema)
	if err != nil {
		t.Fatalf("inspect %s: %v", schema, err)
	}
	data, err := json.MarshalIndent(map[string]any{"specVersion": model.SpecVersion, "model": inspected, "refactor": block}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
