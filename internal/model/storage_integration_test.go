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

// merp fork: a column's storage read from a .pgmodel, inspected, planned and
// applied (merp#1028). The table is merp's blob chunk table, whose bytes are
// already compressed.
const blobChunk = `CREATE TABLE blob_chunk (id uuid NOT NULL, seq integer NOT NULL, bytes bytea NOT NULL,
	CONSTRAINT blob_chunk_pkey PRIMARY KEY (id, seq));`

// TestColumnStorageByModel drives plan and apply with a .pgmodel that sets the
// chunk column's storage: an existing column converges and a second run plans
// nothing, a fresh schema creates the column with it, a model that drops it
// resets the column to its type's default.
func TestColumnStorageByModel(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	dir := t.TempDir()
	external := filepath.Join(dir, "external.pgmodel")
	plain := filepath.Join(dir, "default.pgmodel")
	materialize(t, conn, "merp", blobChunk)
	writeStorageModel(t, conn, "merp", external, "EXTERNAL")
	writeStorageModel(t, conn, "merp", plain, "")
	connArgs := []string{"--host", host, "--port", fmt.Sprint(port), "--db", db, "--user", user, "--password", password, "--sslmode", "disable", "--schema", "merp"}
	planSQL := func(modelPath, name string) (string, bool) {
		out := filepath.Join(dir, name)
		run(t, planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", out)...)
		p := readPlan(t, out)
		return p.ToSQL("raw"), p.HasAnyChanges()
	}
	attstorage := func() string {
		var s string
		if err := conn.QueryRow(`SELECT attstorage::text FROM pg_attribute WHERE attrelid = 'merp.blob_chunk'::regclass AND attname = 'bytes'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	const setExternal = "ALTER COLUMN bytes SET STORAGE EXTERNAL;"

	t.Run("an existing column converges, a second run plans nothing", func(t *testing.T) {
		sqlText, _ := planSQL(external, "converge.json")
		if strings.Count(sqlText, ";") != 1 || !strings.Contains(sqlText, setExternal) {
			t.Fatalf("one SET STORAGE and nothing else:\n%s", sqlText)
		}
		run(t, apply.ApplyCmd, append(connArgs, "--file", external, "--auto-approve")...)
		if got := attstorage(); got != "e" {
			t.Fatalf("attstorage = %q, want e", got)
		}
		if sqlText, changes := planSQL(external, "again.json"); changes {
			t.Errorf("a converged column plans nothing:\n%s", sqlText)
		}
	})

	t.Run("inspection reads the storage and its type's default", func(t *testing.T) {
		inspected, err := ir.NewInspector(conn, nil).BuildIR(context.Background(), "merp")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range inspected.Schemas["merp"].Tables["blob_chunk"].Columns {
			want := ""
			if c.Name == "bytes" {
				want = "external"
			}
			if c.Storage != want {
				t.Errorf("%s: storage %q, want %q", c.Name, c.Storage, want)
			}
		}
	})

	t.Run("a fresh schema creates the column with its storage", func(t *testing.T) {
		materialize(t, conn, "merp", "")
		sqlText, _ := planSQL(external, "create.json")
		if !strings.Contains(sqlText, "CREATE TABLE") || !strings.Contains(sqlText, setExternal) {
			t.Fatalf("CREATE TABLE then SET STORAGE:\n%s", sqlText)
		}
		run(t, apply.ApplyCmd, append(connArgs, "--file", external, "--auto-approve")...)
		if got := attstorage(); got != "e" {
			t.Fatalf("attstorage = %q, want e", got)
		}
		if sqlText, changes := planSQL(external, "created-again.json"); changes {
			t.Errorf("a created column plans nothing:\n%s", sqlText)
		}
	})

	t.Run("a model that drops the storage resets the column to its type's default", func(t *testing.T) {
		sqlText, _ := planSQL(plain, "reset.json")
		if !strings.Contains(sqlText, "ALTER COLUMN bytes SET STORAGE EXTENDED;") {
			t.Fatalf("SET STORAGE EXTENDED:\n%s", sqlText)
		}
		run(t, apply.ApplyCmd, append(connArgs, "--file", plain, "--auto-approve")...)
		if got := attstorage(); got != "x" {
			t.Fatalf("attstorage = %q, want x", got)
		}
	})
}

// TestStorageOutsideSetStorageIsRefused: a word SET STORAGE does not take is
// refused when the model is loaded, naming the column.
func TestStorageOutsideSetStorageIsRefused(t *testing.T) {
	m := &ir.IR{Schemas: map[string]*ir.Schema{"merp": {Name: "merp", Tables: map[string]*ir.Table{
		"blob_chunk": {Schema: "merp", Name: "blob_chunk", Columns: []*ir.Column{{Name: "bytes", Position: 1, DataType: "bytea", Storage: "compressed"}}},
	}}}}
	data, err := json.Marshal(map[string]any{"specVersion": model.SpecVersion, "model": m})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := model.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = doc.DesiredState("merp")
	if err == nil || !strings.Contains(err.Error(), `blob_chunk.bytes: storage "compressed"`) {
		t.Fatalf("want the column named in a refusal, got %v", err)
	}
}

// writeStorageModel inspects schema and writes it as a .pgmodel whose
// blob_chunk.bytes column has the given storage.
func writeStorageModel(t *testing.T, conn *sql.DB, schema, path, storage string) {
	t.Helper()
	inspected, err := ir.NewInspector(conn, nil).BuildIR(context.Background(), schema)
	if err != nil {
		t.Fatalf("inspect %s: %v", schema, err)
	}
	for _, c := range inspected.Schemas[schema].Tables["blob_chunk"].Columns {
		if c.Name == "bytes" {
			c.Storage = storage
		}
	}
	data, err := json.MarshalIndent(map[string]any{"specVersion": model.SpecVersion, "model": inspected}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
