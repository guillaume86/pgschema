package model

// merp fork, patch 8 (fork/README.md): the desired state materialized in the
// target's session. A .pgmodel states every object without free text as the
// inspector prints it, so its IR compares with an inspected current state as
// it is. An object whose definition is free text, a view today, does not: the
// server renders a hand-written SELECT in its own text (pg_get_viewdef drops a
// qualifier, casts a literal, spells BETWEEN out), so the document's text never
// equals the current one. When the current state holds such an object that
// the document names too, the whole document is created as temporary objects
// in one session of the target and read back by the inspector, so both sides
// compare as the server renders them, and the plan still carries the author's
// text (AuthorText).

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/ir"
)

// TextBearing reports whether current holds, in schema, an object of a kind
// whose definition is free text that desired names too. The kinds are the
// views, the one free-text definition a .pgmodel carries today. A plan
// against the database materializes the desired state only then.
func TextBearing(current, desired *ir.IR, schema string) bool {
	cur, des := schemaOf(current, schema), schemaOf(desired, schema)
	if cur == nil || des == nil {
		return false
	}
	for name := range des.Views {
		if cur.Views[name] != nil {
			return true
		}
	}
	return false
}

// Materialize creates the desired state's schema as temporary objects in the
// session of db and inspects them. db must hold one connection: temporary
// objects belong to the session, so every statement and every query of the
// inspector, its concurrent groups included, must reach the same one; and no
// transaction is opened, which would hold that connection away from the
// inspector. The statements are the plan from an empty state
// (diff.GenerateMigrationForTarget), run one by one with pg_temp first on the
// search_path, so every table, view, sequence, index and type lands in the
// session's temporary schema; schema and public follow it, as on the target a
// name the desired state does not hold resolves there. The inspector reads
// the temporary schema with the search_path reset, as on a fresh connection,
// with schema as its managed schema. Materialize returns that IR and the
// temporary schema's name (pg_temp_N), which the caller remaps to schema.
// Nothing survives the session: closing db drops it all.
func Materialize(ctx context.Context, db *sql.DB, desired *ir.IR, schema string, targetMajorVersion int) (*ir.IR, string, error) {
	if err := materializable(schemaOf(desired, schema)); err != nil {
		return nil, "", err
	}

	searchPath := "SET search_path TO pg_temp, " + ir.QuoteIdentifier(schema) + ", public"
	if _, err := db.ExecContext(ctx, searchPath); err != nil {
		return nil, "", fmt.Errorf("materialize the desired state: %w", err)
	}
	for _, d := range diff.GenerateMigrationForTarget(ir.NewIR(), desired, schema, targetMajorVersion) {
		for _, stmt := range d.Statements {
			if _, err := db.ExecContext(ctx, stmt.SQL); err != nil {
				return nil, "", fmt.Errorf("materialize the desired state in pg_temp: %s: %w", head(stmt.SQL), err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, "RESET search_path"); err != nil {
		return nil, "", fmt.Errorf("materialize the desired state: %w", err)
	}

	var temp sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT nspname FROM pg_namespace WHERE oid = pg_my_temp_schema()").Scan(&temp); err != nil && err != sql.ErrNoRows {
		return nil, "", fmt.Errorf("materialize the desired state: %w", err)
	}
	if !temp.Valid || temp.String == "" {
		return nil, "", fmt.Errorf("materialize the desired state: the session holds no temporary schema after creating %q's objects", schema)
	}

	inspector := ir.NewInspector(db, nil)
	inspector.SetManagedSchema(schema)
	inspected, err := inspector.BuildIR(ctx, temp.String)
	if err != nil {
		return nil, "", fmt.Errorf("inspect the materialized desired state in %s: %w", temp.String, err)
	}
	return inspected, temp.String, nil
}

// materializable refuses what cannot be a temporary object: a materialized
// view, a known limitation until the plan may create a scratch schema
// (merp#1847, S6); and a default privilege, whose statement names the real
// schema and would outlive the session.
func materializable(s *ir.Schema) error {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Views))
	for name, view := range s.Views {
		if view.Materialized {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return fmt.Errorf("the desired state holds materialized views (%s), which a plan cannot create in the session's temporary schema", strings.Join(names, ", "))
	}
	if len(s.DefaultPrivileges) > 0 {
		return fmt.Errorf("the desired state holds default privileges, which a plan cannot create in the session's temporary schema")
	}
	return nil
}

// AuthorText puts the document's definition back on each view of the
// materialized state whose rendering differs from the current one, so a plan's
// CREATE and CREATE OR REPLACE VIEW carry the author's text and the rendering
// is read for the comparison alone. A view the current state renders the same
// keeps the rendering, compares equal and plans nothing.
func AuthorText(materialized, document, current *ir.IR, schema string) {
	mat, doc, cur := schemaOf(materialized, schema), schemaOf(document, schema), schemaOf(current, schema)
	if mat == nil || doc == nil {
		return
	}
	for name, view := range mat.Views {
		authored := doc.Views[name]
		if authored == nil {
			continue
		}
		if cur != nil {
			if old := cur.Views[name]; old != nil && old.Definition == view.Definition {
				continue
			}
		}
		view.Definition = authored.Definition
	}
}

func schemaOf(state *ir.IR, schema string) *ir.Schema {
	if state == nil {
		return nil
	}
	return state.Schemas[schema]
}

// head is a statement's first line, to name it in an error.
func head(statement string) string {
	statement = strings.TrimSpace(statement)
	if i := strings.IndexByte(statement, '\n'); i >= 0 {
		return statement[:i] + " ..."
	}
	return statement
}
