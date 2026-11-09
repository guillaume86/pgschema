package model_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	planCmd "github.com/pgplex/pgschema/cmd/plan"
	"github.com/pgplex/pgschema/testutil"
)

// The expand model of a release that adds a required, checked, indexed column
// over rows, and the full model: the second of merp's migration plans is a
// diff of the two, no database.
const (
	expandModel = `
		CREATE TABLE todo_item (id uuid PRIMARY KEY, title text NOT NULL, priority integer);`
	fullModel = `
		CREATE TABLE todo_item (id uuid PRIMARY KEY, title text NOT NULL, priority integer NOT NULL,
			CONSTRAINT todo_item_priority_check CHECK (priority >= 0));
		CREATE INDEX todo_item_priority_idx ON todo_item (priority);`
)

// TestPlanFromTwoDocuments: --source reads the current state from a document,
// so the plan needs no connection and carries no fingerprint; --in-transaction
// makes the same diff one group of plain statements.
func TestPlanFromTwoDocuments(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, _, _, _, _, _ := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	dir := t.TempDir()
	expand, full := filepath.Join(dir, "expand.pgmodel"), filepath.Join(dir, "full.pgmodel")
	materialize(t, conn, "merp", expandModel)
	writeModel(t, conn, "merp", expand)
	materialize(t, conn, "merp", fullModel)
	writeModel(t, conn, "merp", full)

	// No --host, --db or --user: nothing is read from a database.
	planTwo := func(name string, extra ...string) (string, map[string]any, error) {
		out := filepath.Join(dir, name)
		args := append([]string{"--schema", "merp", "--source", expand, "--file", full, "--output-json", out}, extra...)
		if err := execute(planCmd.PlanCmd, args...); err != nil {
			return "", nil, err
		}
		return out, decodeObject(t, readFile(t, out)), nil
	}

	t.Run("in a transaction: one group, plain statements, no fingerprint", func(t *testing.T) {
		path, top, err := planTwo("tx.json", "--in-transaction")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := top["source_fingerprint"]; ok {
			t.Error("a plan made from two documents has no fingerprint")
		}
		p := readPlan(t, path)
		if len(p.Groups) != 1 {
			t.Fatalf("one execution group, got %d", len(p.Groups))
		}
		sql := p.ToSQL("raw")
		for _, want := range []string{"SET NOT NULL", "CHECK", "CREATE INDEX"} {
			if !strings.Contains(sql, want) {
				t.Errorf("plan lacks %q:\n%s", want, sql)
			}
		}
		if strings.Contains(sql, "CONCURRENTLY") || strings.Contains(sql, "NOT VALID") || strings.Contains(sql, "VALIDATE") || strings.Contains(sql, "pg_stat_activity") {
			t.Errorf("no concurrent form, no wait, in a transaction:\n%s", sql)
		}
		for _, s := range p.Groups[0].Steps {
			if s.Directive != nil {
				t.Errorf("no step carries a directive: %+v", s)
			}
		}
	})

	t.Run("without the flag the online rewrites apply, so the flag is what removes them", func(t *testing.T) {
		path, top, err := planTwo("online.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := top["source_fingerprint"]; ok {
			t.Error("a plan made from two documents has no fingerprint")
		}
		p := readPlan(t, path)
		if len(p.Groups) < 2 || !strings.Contains(p.ToSQL("raw"), "CONCURRENTLY") {
			t.Errorf("the default form rewrites online:\n%s", p.ToSQL("raw"))
		}
	})

	t.Run("a document against itself plans nothing", func(t *testing.T) {
		out := filepath.Join(dir, "same.json")
		if err := execute(planCmd.PlanCmd, "--schema", "merp", "--source", full, "--file", full, "--in-transaction", "--output-json", out); err != nil {
			t.Fatal(err)
		}
		if readPlan(t, out).HasAnyChanges() {
			t.Error("equal documents plan nothing")
		}
	})

	t.Run("a plan between two documents renames nothing: an entry whose old object the source holds is refused", func(t *testing.T) {
		renamed := filepath.Join(dir, "renamed.pgmodel")
		materialize(t, conn, "merp", strings.ReplaceAll(fullModel, "todo_item", "todo_task"))
		writeRefactorModel(t, conn, "merp", renamed, []map[string]string{
			{"op": "rename", "kind": "table", "from": "merp.todo_item", "to": "merp.todo_task"},
			{"op": "rename", "kind": "constraint", "from": "merp.todo_task.todo_item_pkey", "to": "merp.todo_task.todo_task_pkey"},
			{"op": "rename", "kind": "constraint", "from": "merp.todo_task.todo_item_priority_check", "to": "merp.todo_task.todo_task_priority_check"},
			{"op": "rename", "kind": "index", "from": "merp.todo_item_priority_idx", "to": "merp.todo_task_priority_idx"},
		})
		out := filepath.Join(dir, "renames.json")
		err := execute(planCmd.PlanCmd, "--schema", "merp", "--source", full, "--file", renamed, "--in-transaction", "--output-json", out)
		want := "refused: rename table merp.todo_item to merp.todo_task: a plan between two documents renames nothing. Nothing was planned."
		if err == nil || err.Error() != want {
			t.Fatalf("expected %q, got %v", want, err)
		}
		if _, statErr := os.Stat(out); statErr == nil {
			t.Error("no plan is written")
		}

		// Old names absent from the source: every entry inert, the diff as without a block.
		inert := filepath.Join(dir, "inert.json")
		if err := execute(planCmd.PlanCmd, "--schema", "merp", "--source", renamed, "--file", renamed, "--in-transaction", "--output-json", inert); err != nil {
			t.Fatal(err)
		}
		if readPlan(t, inert).HasAnyChanges() {
			t.Error("a document against itself plans nothing, its renames inert")
		}
		if text := readFile(t, inert); !strings.Contains(text, `"applied": false`) || strings.Contains(text, `"applied": true`) {
			t.Errorf("the plan JSON reports every entry inert:\n%s", text)
		}
	})

	t.Run("a SQL --file is refused with --source or --in-transaction", func(t *testing.T) {
		sqlFile := filepath.Join(dir, "x.sql")
		if err := os.WriteFile(sqlFile, []byte(fullModel), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, flag := range [][]string{{"--source", expand}, {"--in-transaction"}} {
			err := execute(planCmd.PlanCmd, append([]string{"--schema", "merp", "--file", sqlFile, "--host", "localhost", "--db", "d", "--user", "u"}, flag...)...)
			if err == nil || !strings.Contains(err.Error(), ".pgmodel document") {
				t.Errorf("%v: expected the refusal, got %v", flag, err)
			}
		}
	})
}

// TestInTransactionPlanFromDatabase: --in-transaction on the database-backed
// plan keeps the fingerprint (apply's drift check) and changes only the form.
func TestInTransactionPlanFromDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	dir := t.TempDir()
	full := filepath.Join(dir, "full.pgmodel")
	materialize(t, conn, "merp", fullModel)
	writeModel(t, conn, "merp", full)
	materialize(t, conn, "merp", expandModel)
	connArgs := []string{"--host", host, "--port", fmt.Sprint(port), "--db", db, "--user", user, "--password", password, "--sslmode", "disable", "--schema", "merp", "--file", full}

	plan := func(name string, extra ...string) (string, map[string]any) {
		out := filepath.Join(dir, name)
		run(t, planCmd.PlanCmd, append(append([]string{}, connArgs...), append(extra, "--output-json", out)...)...)
		return out, decodeObject(t, readFile(t, out))
	}

	path, top := plan("tx.json", "--in-transaction")
	if _, ok := top["source_fingerprint"]; !ok {
		t.Error("the database-backed plan keeps its fingerprint")
	}
	p := readPlan(t, path)
	sql := p.ToSQL("raw")
	if len(p.Groups) != 1 || strings.Contains(sql, "CONCURRENTLY") || strings.Contains(sql, "pg_stat_activity") || !strings.Contains(sql, "CREATE INDEX") {
		t.Errorf("one group of plain statements, got %d groups:\n%s", len(p.Groups), sql)
	}

	path, _ = plan("online.json")
	if sql := readPlan(t, path).ToSQL("raw"); !strings.Contains(sql, "CONCURRENTLY") {
		t.Errorf("the default form is unchanged:\n%s", sql)
	}
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatal(err)
	}
	return top
}
