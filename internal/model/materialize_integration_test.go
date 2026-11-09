package model_test

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	planCmd "github.com/pgplex/pgschema/cmd/plan"
	"github.com/pgplex/pgschema/internal/model"
	"github.com/pgplex/pgschema/ir"
	"github.com/pgplex/pgschema/testutil"
)

// TestDocumentIrEqualsInspectedIr is the invariant fact of patch 8
// (materialize.go, merp#1847 S8): for every .pgmodel under testdata, the IR
// inspected from the desired state materialized in the session's pg_temp,
// remapped to the document's schema, equals the document's IR on every object
// without free text. A view's definition is free text, so a view compares by
// its name alone. The empty second plan stays the oracle; this fact holds the
// materialization changes nothing else a plan reads.
func TestDocumentIrEqualsInspectedIr(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	var paths []string
	err := filepath.WalkDir(filepath.Join("..", "..", "testdata"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && model.IsDocumentPath(path) {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no .pgmodel under testdata")
	}

	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()
	major, err := testutil.GetMajorVersion(conn)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), model.Extension), func(t *testing.T) {
			doc, err := model.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Config == nil || doc.Config.Schema == "" {
				t.Fatalf("%s names no config.schema", path)
			}
			schema := doc.Config.Schema
			document, err := doc.DesiredState(schema)
			if err != nil {
				t.Fatal(err)
			}
			desired, err := doc.DesiredState(schema)
			if err != nil {
				t.Fatal(err)
			}

			config := &planCmd.PlanConfig{Host: host, Port: port, DB: db, User: user, Password: password,
				Schema: schema, ApplicationName: "pgschema", SSLMode: "disable"}
			start := time.Now()
			inspected, err := planCmd.MaterializeDesired(config, desired, major)
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			t.Logf("%d tables materialized and inspected in %s", len(document.Schemas[schema].Tables), time.Since(start).Round(time.Millisecond))

			for _, mismatch := range mismatches(t, document.Schemas[schema], inspected.Schemas[schema]) {
				t.Error(mismatch)
			}
		})
	}

	var outside int
	if err := conn.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_t%'`).Scan(&outside); err != nil {
		t.Fatal(err)
	}
	if outside != 0 {
		t.Errorf("a materialization made %d relations outside the session's temporary schema", outside)
	}
}

// mismatches lists the objects of two states of one schema that differ: each
// table by its JSON, each view by its name, then the rest of the schema.
func mismatches(t *testing.T, document, inspected *ir.Schema) []string {
	t.Helper()
	if inspected == nil {
		return []string{"the inspected state has no schema " + document.Name}
	}
	dropImpliedPrecision(document)
	dropImpliedPrecision(inspected)
	var out []string
	for _, name := range union(keys(document.Tables), keys(inspected.Tables)) {
		if d, i := marshal(t, document.Tables[name]), marshal(t, inspected.Tables[name]); d != i {
			out = append(out, "table "+name+"\n--- document ---\n"+d+"\n--- inspected ---\n"+i)
		}
	}
	if d, i := strings.Join(keys(document.Views), ", "), strings.Join(keys(inspected.Views), ", "); d != i {
		out = append(out, "views: the document names ["+d+"], the inspected state ["+i+"]")
	}
	rest := func(s *ir.Schema) string {
		return marshal(t, map[string]any{
			"functions": s.Functions, "procedures": s.Procedures, "aggregates": s.Aggregates,
			"sequences": s.Sequences, "types": s.Types, "default_privileges": s.DefaultPrivileges,
			"privileges": s.Privileges, "column_privileges": s.ColumnPrivileges,
			"revoked_default_privileges": s.RevokedDefaultPrivileges,
		})
	}
	if d, i := rest(document), rest(inspected); d != i {
		out = append(out, "the schema's other objects\n--- document ---\n"+d+"\n--- inspected ---\n"+i)
	}
	return out
}

// integerBits is the precision information_schema reports for an integer
// column (numeric_precision, scale 0): the type implies it and the differ
// never reads it (comparableColumnType reads a precision for numeric alone),
// so a .pgmodel may leave it out, as merp's deriver does.
var integerBits = map[string]int{"smallint": 16, "integer": 32, "bigint": 64}

// dropImpliedPrecision clears the precision and scale an integer column's type
// implies, so the fact compares the type.
func dropImpliedPrecision(s *ir.Schema) {
	for _, table := range s.Tables {
		for _, c := range table.Columns {
			bits, ok := integerBits[c.DataType]
			if ok && c.Precision != nil && *c.Precision == bits && (c.Scale == nil || *c.Scale == 0) {
				c.Precision, c.Scale = nil, nil
			}
		}
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func keys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range append(a, b...) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
