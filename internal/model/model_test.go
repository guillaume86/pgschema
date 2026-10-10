package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodModel = `{
  "metadata": {"database_version": "PostgreSQL 18"},
  "schemas": {
    "merp": {
      "name": "merp",
      "owner": "",
      "tables": {
        "item": {
          "schema": "merp",
          "name": "item",
          "type": "BASE_TABLE",
          "columns": [
            {"name": "id", "position": 1, "data_type": "integer", "is_nullable": false,
             "default_value": "nextval('item_id_seq'::regclass)"}
          ],
          "constraints": {
            "item_pkey": {"schema": "merp", "table": "item", "name": "item_pkey", "type": "PRIMARY_KEY",
                          "columns": [{"name": "id", "position": 1}], "is_valid": true}
          },
          "indexes": null,
          "rls_enabled": false,
          "rls_forced": false,
          "dependencies": null,
          "is_partitioned": false
        }
      },
      "sequences": {
        "item_id_seq": {"schema": "merp", "name": "item_id_seq", "data_type": "integer", "start_value": 1,
                        "increment": 1, "cycle_option": false, "owned_by_table": "item", "owned_by_column": "id"}
      }
    }
  }
}`

func doc(fields string) string {
	return "{" + fields + "}"
}

func parseGood(t *testing.T) *Document {
	t.Helper()
	d, err := Parse([]byte(doc(`"specVersion": "1.0", "model": ` + goodModel)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return d
}

func TestDesiredStateFillsEmptyCollections(t *testing.T) {
	desired, err := parseGood(t).DesiredState("merp")
	if err != nil {
		t.Fatalf("DesiredState: %v", err)
	}
	table := desired.Schemas["merp"].Tables["item"]
	if table.Indexes == nil || table.Triggers == nil || table.Policies == nil || desired.Schemas["merp"].Views == nil {
		t.Errorf("null or omitted collections should read as empty maps, as the inspector builds them")
	}
}

func TestDesiredStateMarksSerialColumns(t *testing.T) {
	desired, err := parseGood(t).DesiredState("merp")
	if err != nil {
		t.Fatalf("DesiredState: %v", err)
	}
	if !desired.Schemas["merp"].Tables["item"].Columns[0].IsSerial {
		t.Errorf("normalization should mark id as a SERIAL column (derived, not serialized)")
	}
}

func TestDesiredStateRefusesUndeclaredSchema(t *testing.T) {
	_, err := parseGood(t).DesiredState("public")
	if err == nil || !strings.Contains(err.Error(), `declares no schema "public" (it declares: "merp")`) {
		t.Fatalf("expected the refusal to name the declared schemas, got %v", err)
	}
}

func TestDesiredStateKeepsOnlyTheScopedSchema(t *testing.T) {
	m := strings.Replace(goodModel, `"schemas": {`, `"schemas": {"other": {"name": "other", "owner": ""},`, 1)
	d, err := Parse([]byte(doc(`"specVersion": "1.0", "model": ` + m)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	desired, err := d.DesiredState("merp")
	if err != nil {
		t.Fatalf("DesiredState: %v", err)
	}
	if len(desired.Schemas) != 1 || desired.Schemas["merp"] == nil {
		t.Errorf("desired state should hold only the scoped schema, got %d schemas", len(desired.Schemas))
	}
}

func TestParseAcceptsMinorVersions(t *testing.T) {
	if _, err := Parse([]byte(doc(`"specVersion": "1.7", "model": ` + goodModel))); err != nil {
		t.Fatalf("a 1.x document should be accepted: %v", err)
	}
}

func TestParseRefusals(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"missing specVersion", `"model": ` + goodModel, "missing specVersion"},
		{"missing model", `"specVersion": "1.0"`, "missing model"},
		{"null model", `"specVersion": "1.0", "model": null`, "missing model"},
		{"wrong specVersion", `"specVersion": "2.0", "model": ` + goodModel, `unsupported .pgmodel specVersion "2.0": this build supports 1.0`},
		{"refactor", `"specVersion": "1.0", "model": ` + goodModel + `, "refactor": [{"id": "r1", "op": "rename", "kind": "table", "from": "merp.a", "to": "merp.b"}]`, "rename directives are not supported in this build"},
		{"unknown envelope field", `"specVersion": "1.0", "model": ` + goodModel + `, "refactors": []`, `unknown field "refactors"`},
		{"unknown IR field", `"specVersion": "1.0", "model": {"schemas": {"merp": {"name": "merp", "not_an_ir_field": {}}}}`, `unknown field "not_an_ir_field"`},
		{"not JSON", `"specVersion": `, "invalid .pgmodel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(doc(tc.body)))
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestReadNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pgmodel")
	if err := os.WriteFile(path, []byte(`{"model": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Read(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "missing specVersion") {
		t.Fatalf("expected the path and the reason, got %v", err)
	}
}

func TestIsDocumentPath(t *testing.T) {
	for path, want := range map[string]bool{"app.pgmodel": true, "dir/APP.PGMODEL": true, "schema.sql": false, "app.pgmodel.json": false} {
		if got := IsDocumentPath(path); got != want {
			t.Errorf("IsDocumentPath(%q) = %v, want %v", path, got, want)
		}
	}
}
