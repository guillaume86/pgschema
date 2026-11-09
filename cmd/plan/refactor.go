package plan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pgplex/pgschema/internal/diff"
	"github.com/pgplex/pgschema/internal/refactor"
	"github.com/pgplex/pgschema/ir"
)

// This file is patch 3 of the merp fork (see fork/README.md and
// internal/refactor): a rename is run by the server in the plan's session and
// read back, never rewritten by the engine.

// refactorSource is a desired-state source that carries rename directives
// (the merp fork's .pgmodel provider).
type refactorSource interface {
	Refactor() []refactor.Entry
}

func refactorEntries(source desiredIRSource) []refactor.Entry {
	if rs, ok := source.(refactorSource); ok {
		return rs.Refactor()
	}
	return nil
}

// sessionPlan is what a database plan diffs: the current state (the committed
// catalogue, or the catalogue as the renames left it in the plan's session),
// the desired state (materialized when due) and the rename steps that run
// first, with the plan JSON's "refactor" report.
type sessionPlan struct {
	current, desired *ir.IR
	renames          []diff.Diff
	report           json.RawMessage
}

// planInSession decides the source's rename entries on the committed
// inspection. When none applies, the committed state is the current one and
// the desired state is materialized as patch 8 says, in a session of its own.
// When one applies, one session on the target runs BEGIN as a statement, the
// entries' statements in order and the inspector, whose IR is the current
// state; the materialization, when due, runs in the same session; ROLLBACK
// ends it on every path, so the target is as committed and the renamed tables
// are locked only while the catalogue is read back. committed is moved by name
// (refactor.Apply) and must not be diffed after a rename applied.
func planInSession(config *PlanConfig, source desiredIRSource, ignoreConfig *ir.IgnoreConfig, committed, desired *ir.IR, targetMajorVersion int) (*sessionPlan, error) {
	entries := refactorEntries(source)
	var renames []diff.Diff
	var report json.RawMessage
	if len(entries) > 0 {
		var results []refactor.Result
		var err error
		if renames, results, err = refactor.Apply(committed, config.Schema, entries); err != nil {
			return nil, err
		}
		if report, err = json.Marshal(results); err != nil {
			return nil, err
		}
	}
	if len(renames) == 0 {
		desired, err := materializeDesired(config, nil, committed, desired, targetMajorVersion)
		if err != nil {
			return nil, err
		}
		return &sessionPlan{current: committed, desired: desired, report: report}, nil
	}

	db, err := openSession(config)
	if err != nil {
		return nil, fmt.Errorf("run the renames in the plan's session: %w", err)
	}
	defer db.Close()
	current, desired, err := inspectRenamed(context.Background(), db, renames, config, ignoreConfig, desired, targetMajorVersion)
	if err != nil {
		return nil, err
	}
	return &sessionPlan{current: current, desired: desired, renames: renames, report: report}, nil
}

// inspectRenamed runs the renames in a transaction of db's one session, reads
// the catalogue back and materializes the desired state there when due, then
// rolls back. BEGIN and the renames run on the session pinned (db.Conn), so a
// lost connection fails them instead of running a rename outside the
// transaction; before the rollback the session must still be that backend in
// that transaction, or the plan fails.
func inspectRenamed(ctx context.Context, db *sql.DB, renames []diff.Diff, config *PlanConfig, ignoreConfig *ir.IgnoreConfig, desired *ir.IR, targetMajorVersion int) (current, materialized *ir.IR, err error) {
	defer func() {
		if _, rollbackErr := db.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil && err == nil {
			current, materialized, err = nil, nil, fmt.Errorf("roll the plan's renames back: %w", rollbackErr)
		}
	}()
	pid, err := beginRenames(ctx, db, renames)
	if err != nil {
		return nil, nil, err
	}

	current, err = ir.NewInspector(db, ignoreConfig).BuildIR(ctx, config.Schema)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect the renamed state in the plan's session: %w", err)
	}
	if materialized, err = materializeDesired(config, db, current, desired, targetMajorVersion); err != nil {
		return nil, nil, err
	}

	var after int
	var inTransaction bool
	if err := db.QueryRowContext(ctx, "SELECT pg_backend_pid(), txid_current_if_assigned() IS NOT NULL").Scan(&after, &inTransaction); err != nil {
		return nil, nil, fmt.Errorf("read the plan's session back: %w", err)
	}
	if after != pid || !inTransaction {
		return nil, nil, fmt.Errorf("the plan's session was lost while reading the renamed state back (backend %d, now %d); nothing was planned", pid, after)
	}
	return current, materialized, nil
}

// beginRenames pins the session, reads its backend pid, opens the transaction
// and runs the renames in order, then hands the session back to db's pool.
func beginRenames(ctx context.Context, db *sql.DB, renames []diff.Diff) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("run the renames in the plan's session: %w", err)
	}
	defer conn.Close()
	var pid int
	if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return 0, fmt.Errorf("run the renames in the plan's session: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return 0, fmt.Errorf("run the renames in the plan's session: %w", err)
	}
	for _, d := range renames {
		for _, stmt := range d.Statements {
			if _, err := conn.ExecContext(ctx, stmt.SQL); err != nil {
				return 0, fmt.Errorf("run the renames in the plan's session: %s: %w", stmt.SQL, err)
			}
		}
	}
	return pid, nil
}

// refuseRenames is the refactor block against a current state read from a
// document (--source): a plan between two documents renames nothing, so an
// entry whose old object the source holds is refused; an absent one is
// reported inert.
func refuseRenames(source desiredIRSource, current *ir.IR, schema string) (json.RawMessage, error) {
	entries := refactorEntries(source)
	if len(entries) == 0 {
		return nil, nil
	}
	results, err := refactor.RefuseOnDocument(current, schema, entries)
	if err != nil {
		return nil, err
	}
	return json.Marshal(results)
}
