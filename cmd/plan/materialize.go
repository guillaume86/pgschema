package plan

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pgplex/pgschema/cmd/util"
	"github.com/pgplex/pgschema/internal/model"
	"github.com/pgplex/pgschema/ir"
)

// This file is patch 8 of the merp fork (see fork/README.md and
// internal/model/materialize.go): a database plan from a .pgmodel compares a
// view as the server renders it on both sides.

// materializeDesired is the desired state a database plan from a .pgmodel
// diffs: the document's IR as it is, unless the current state holds a view
// the document names too (model.TextBearing); then the document materialized
// in the target's session, each view carrying the author's text wherever its
// rendering differs from the current one (model.AuthorText). session is the
// plan's session when patch 3 opened one for the renames (refactor.go), the
// materialization running there inside its transaction; nil, it opens its own
// (MaterializeDesired). A plan between two documents (--source) never comes
// here.
func materializeDesired(config *PlanConfig, session *sql.DB, current, desired *ir.IR, targetMajorVersion int) (*ir.IR, error) {
	if !model.TextBearing(current, desired, config.Schema) {
		return desired, nil
	}
	var materialized *ir.IR
	var err error
	if session == nil {
		materialized, err = MaterializeDesired(config, desired, targetMajorVersion)
	} else {
		materialized, err = materializeIn(session, desired, config.Schema, targetMajorVersion)
	}
	if err != nil {
		return nil, err
	}
	model.AuthorText(materialized, desired, current, config.Schema)
	return materialized, nil
}

// MaterializeDesired opens one session on the target config names and
// materializes desired there (materializeIn). Closing the connection drops
// what it made. Exported for the fork's invariant fact (internal/model,
// TestDocumentIrEqualsInspectedIr).
func MaterializeDesired(config *PlanConfig, desired *ir.IR, targetMajorVersion int) (*ir.IR, error) {
	db, err := openSession(config)
	if err != nil {
		return nil, fmt.Errorf("materialize the desired state: %w", err)
	}
	defer db.Close()
	return materializeIn(db, desired, config.Schema, targetMajorVersion)
}

// materializeIn creates desired in the session db holds as its temporary
// objects, inspects them (model.Materialize) and remaps the temporary schema
// to schema with upstream's normalizeSchemaNames, the remap of a plan
// database's temporary schema.
func materializeIn(db *sql.DB, desired *ir.IR, schema string, targetMajorVersion int) (*ir.IR, error) {
	materialized, temp, err := model.Materialize(context.Background(), db, desired, schema, targetMajorVersion)
	if err != nil {
		return nil, err
	}
	normalizeSchemaNames(materialized, temp, schema)
	stripSchemaFromViews(materialized, schema)
	return materialized, nil
}

// openSession opens one session on the target config names: a pool of one
// connection, so every statement and every query of the inspector, its
// concurrent groups included, reaches the same backend.
func openSession(config *PlanConfig) (*sql.DB, error) {
	sslmode := config.SSLMode
	if sslmode == "" {
		sslmode = "prefer"
	}
	db, err := util.Connect(&util.ConnectionConfig{
		Host:            config.Host,
		Port:            config.Port,
		Database:        config.DB,
		User:            config.User,
		Password:        config.Password,
		SSLMode:         sslmode,
		ApplicationName: config.ApplicationName,
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// stripSchemaFromViews renders each materialized view's reference to schema as
// the target renders it. The inspector prints a view under a search_path of
// the view's own schema: in the session that is pg_temp_N, so a relation the
// document does not hold, which resolves to the target's own (a foreign table
// merp makes before the first plan, merp#1860), prints as schema.name; on the
// target it prints bare, and upstream's normalizeView strips schema there.
// The same strip on this side compares both renderings alike; it cannot
// change what a reference means, since the remap already reads pg_temp_N as
// schema, the one schema both names resolve to on the target.
func stripSchemaFromViews(state *ir.IR, schema string) {
	s := state.Schemas[schema]
	if s == nil {
		return
	}
	for _, view := range s.Views {
		view.Definition = ir.StripSchemaPrefixFromBody(view.Definition, schema)
	}
}
