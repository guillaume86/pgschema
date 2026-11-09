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

// merp fork, patch 8 (materialize.go, merp#1850): a .pgmodel view whose
// definition is a hand-written SELECT, which pg_get_viewdef renders in another
// text (the alias qualifier dropped, literals cast, BETWEEN, IN and LIKE
// spelled out), converges: applied, then planned again, it plans nothing. A
// plan that replaces it carries the author's text.
const saleTable = `CREATE TABLE tests_reporting_sale (id uuid PRIMARY KEY, tenant uuid NOT NULL, region text NOT NULL,
	amount numeric NOT NULL, cost numeric NOT NULL, sold_on timestamptz NOT NULL, sold_day date NOT NULL, code varchar(10));`

// saleTotal is merp's test module SELECT (tests/TestModules/Reporting/sale-total.sql).
const saleTotal = `select md5(s.tenant::text || '|' || s.region)::uuid as id,
       s.tenant,
       s.region,
       sum(s.amount) as total,
       count(*)::integer as sales,
       sum(s.amount - s.cost) as margin
from merp.tests_reporting_sale s
group by s.tenant, s.region`

// saleReport has a sales report's constructs: extract, date_trunc, a rank
// over an aggregate and a window over an aggregate.
const saleReport = `select s.tenant, s.region,
       extract(year from s.sold_on)::int as year,
       date_trunc('month', s.sold_on) as month,
       sum(s.amount) as total,
       rank() over (partition by s.tenant order by sum(s.amount) desc) as rank,
       sum(sum(s.amount)) over (partition by s.tenant order by date_trunc('month', s.sold_on)) as running
from merp.tests_reporting_sale s
where s.amount > 0 and s.region = 'EU'
group by s.tenant, s.region, extract(year from s.sold_on), date_trunc('month', s.sold_on)`

// saleCasts has what the database rewrites or casts: a date and a varchar
// column, an untyped date literal, BETWEEN, IN and LIKE.
const saleCasts = `select s.id, date_trunc('month', s.sold_day) as m, extract(year from s.sold_day)::int as y, s.code
from merp.tests_reporting_sale s
where s.code = 'X' and s.sold_day > '2024-01-01' and s.amount between 1 and 10.5
  and s.region in ('a', 'b') and s.region like 'E%'`

func TestHandWrittenViewsConvergeByModel(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	defer pg.Stop()
	conn, host, port, db, user, password := testutil.ConnectToPostgres(t, pg)
	defer conn.Close()

	dir := t.TempDir()
	connArgs := []string{"--host", host, "--port", fmt.Sprint(port), "--db", db, "--user", user, "--password", password, "--sslmode", "disable", "--schema", "merp"}
	planSQL := func(modelPath, name string) (string, bool) {
		out := filepath.Join(dir, name)
		run(t, planCmd.PlanCmd, append(connArgs, "--file", modelPath, "--output-json", out)...)
		p := readPlan(t, out)
		return p.ToSQL("raw"), p.HasAnyChanges()
	}
	views := map[string]string{"sale_total": saleTotal, "sale_report": saleReport, "sale_casts": saleCasts}
	with := func(name, definition string) map[string]string {
		changed := map[string]string{name: definition}
		for n, d := range views {
			if n != name {
				changed[n] = d
			}
		}
		return changed
	}

	materialize(t, conn, "merp", saleTable)
	written := filepath.Join(dir, "views.pgmodel")
	writeViewModel(t, conn, "merp", written, "", views)
	if sqlText, _ := planSQL(written, "create.json"); strings.Count(sqlText, "CREATE OR REPLACE VIEW") != 3 {
		t.Fatalf("the three views created:\n%s", sqlText)
	}
	run(t, apply.ApplyCmd, append(connArgs, "--file", written, "--auto-approve")...)

	t.Run("a hand-written SELECT applied plans nothing again", func(t *testing.T) {
		if sqlText, changes := planSQL(written, "again.json"); changes {
			t.Errorf("a converged view plans nothing:\n%s", sqlText)
		}
	})

	t.Run("a changed predicate plans the view's CREATE OR REPLACE", func(t *testing.T) {
		path := filepath.Join(dir, "predicate.pgmodel")
		writeViewModel(t, conn, "merp", path, "", with("sale_report", strings.Replace(saleReport, "s.amount > 0", "s.amount > 10", 1)))
		sqlText, _ := planSQL(path, "predicate.json")
		if strings.Count(sqlText, ";") != 1 || !strings.Contains(sqlText, "CREATE OR REPLACE VIEW sale_report") || !strings.Contains(sqlText, "amount > 10") {
			t.Errorf("one CREATE OR REPLACE VIEW sale_report with the new predicate:\n%s", sqlText)
		}
	})

	t.Run("a column added plans the view's CREATE OR REPLACE", func(t *testing.T) {
		path := filepath.Join(dir, "column.pgmodel")
		writeViewModel(t, conn, "merp", path, "", with("sale_total", strings.Replace(saleTotal, " as margin", " as margin,\n       max(s.amount) as top", 1)))
		sqlText, _ := planSQL(path, "column.json")
		if strings.Count(sqlText, ";") != 1 || !strings.Contains(sqlText, "CREATE OR REPLACE VIEW sale_total") || !strings.Contains(sqlText, "max(s.amount) as top") {
			t.Errorf("one CREATE OR REPLACE VIEW sale_total with the new column:\n%s", sqlText)
		}
	})

	t.Run("a SELECT over a column the plan adds keeps its text and converges once applied", func(t *testing.T) {
		path := filepath.Join(dir, "added.pgmodel")
		writeViewModel(t, conn, "merp", path, "note text", with("sale_casts", strings.Replace(saleCasts, "s.code\n", "s.code, s.note\n", 1)))
		sqlText, _ := planSQL(path, "added.json")
		add := strings.Index(sqlText, "ADD COLUMN note")
		replace := strings.Index(sqlText, "CREATE OR REPLACE VIEW sale_casts")
		if add < 0 || replace < add || !strings.Contains(sqlText, "s.note") {
			t.Fatalf("ADD COLUMN then the hand-written CREATE OR REPLACE VIEW:\n%s", sqlText)
		}
		run(t, apply.ApplyCmd, append(connArgs, "--file", path, "--auto-approve")...)
		if sqlText, changes := planSQL(path, "added-again.json"); changes {
			t.Errorf("the replaced view plans nothing again:\n%s", sqlText)
		}
	})
}

// writeViewModel inspects schema, adds a column to tests_reporting_sale when
// column is set ("name type"), declares views by their hand-written
// definitions and writes it as a .pgmodel.
func writeViewModel(t *testing.T, conn *sql.DB, schema, path, column string, views map[string]string) {
	t.Helper()
	inspected, err := ir.NewInspector(conn, nil).BuildIR(context.Background(), schema)
	if err != nil {
		t.Fatalf("inspect %s: %v", schema, err)
	}
	s := inspected.Schemas[schema]
	if column != "" {
		table := s.Tables["tests_reporting_sale"]
		name, dataType, _ := strings.Cut(column, " ")
		table.Columns = append(table.Columns, &ir.Column{Name: name, Position: len(table.Columns) + 1, DataType: dataType, IsNullable: true})
	}
	s.Views = map[string]*ir.View{}
	for name, definition := range views {
		s.Views[name] = &ir.View{Schema: schema, Name: name, Definition: definition}
	}
	data, err := json.MarshalIndent(map[string]any{"specVersion": model.SpecVersion, "model": inspected}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
