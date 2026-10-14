# The merp fork of pgschema

This branch (`merp`) is a lean fork of upstream [pgplex/pgschema](https://github.com/pgplex/pgschema):
upstream at a release tag plus a short patch series on top. It replaces `pgproj`, the earlier
41k-line fork of pgschema 1.9 that was never rebased. The goal is to stay easy to sync: every
upstream release is a `git rebase` away.

## Consumer

The only consumer is merp's ERP (`erp/` in the merp workspace). Its client
(`erp/src/lib/Pgschema/PgschemaClient.cs`) runs two verbs, `plan` and `apply`, on one schema, with the
desired state derived from its module declarations as a `.pgmodel` document. That model holds
tables, columns, PK/FK/UNIQUE/CHECK constraints and indexes only. Everything else pgschema does
stays upstream's and is not exercised by merp.

## Patches

| # | Patch | New files | Upstream files hooked |
|---|---|---|---|
| 1 | **Model source.** `plan --file x.pgmodel` and `apply --file x.pgmodel` read the desired state from a `.pgmodel` (upstream's IR as JSON) and hand it straight to the differ. A SQL `--file` boots an embedded Postgres to materialize the desired state on every call; a `.pgmodel` starts nothing. The provider is selected by the file extension; there is no new flag. | `internal/model/` (document reader, the third `DesiredStateProvider`, tests), `cmd/plan/model.go` (`generatePlanFromIR`), `ir/model_normalize.go` (exports the inspector's normalization) | `cmd/plan/plan.go` only (7 added lines, none modified) |
| 2 | **CI off.** Every workflow's `on:` block is `workflow_dispatch` only: nothing runs on push, pull request, schedule or release, each workflow still runs by hand. | none | `.github/workflows/ci-test.yml`, `.github/workflows/docker-latest.yml`, `.github/workflows/release.yml` |

Every hook carries a `merp fork` comment: `git grep -n "merp fork"` lists them all.

```sh
pgschema plan  --host H --port P --db D --user U --schema merp --file app.pgmodel --output-json plan.json
pgschema apply --host H --port P --db D --user U --schema merp --file app.pgmodel --auto-approve
```

### Patch 1 hooks

`cmd/plan/plan.go`
- The `internal/model` import.
- `CreateDesiredStateProvider` returns `model.NewProvider` when `config.File` ends in `.pgmodel`
  (case-insensitive), before the target version probe, so no database is touched to build it.
- `GeneratePlan` hands a provider that already holds IR (`desiredIRSource`) to
  `generatePlanFromIR`.

`cmd/apply/apply.go` is untouched: `RunApply` passes `--file` through `PlanConfig.File` to
`CreateDesiredStateProvider` and `ApplyMigration` to `GeneratePlan`, so both hooks above serve it.
Upstream's `validateFileExtension` only refuses `.json` for `--file`, so a `.pgmodel` passes (and a
document must be named `*.pgmodel`, not `*.pgmodel.json`).

### The `.pgmodel` document (spec version 1.0)

```json
{ "specVersion": "1.0", "model": <ir.IR JSON>, "refactor": [], "config": { "schema": "...", "ignore": ... } }
```

- `specVersion` and `model` are required. Any `1.x` is accepted; another major is refused, naming
  the version this build supports.
- The envelope and `model` are decoded strictly: an unknown field is an error, so a misspelled key
  (`refactors`) cannot silently drop rename directives, and a document written for another IR shape
  fails at load instead of planning against a truncated state.
- `refactor` must be absent or empty: rename directives are not supported in this build.
- `config` is informational and never re-applied. Scope comes from `--schema`, as for `--file`;
  the model must declare that schema. Only that schema is planned.
- The model is assumed already filtered for ignores by its producer; a `.pgschemaignore` in the
  working directory still filters the current state, as upstream.
- The loaded schema goes through the inspector's normalization (`ir.NormalizeModel`), so it compares
  with an inspected current state and derived fields (`IsSerial`) are set.
- The source fingerprint is computed from the target as for `--file`, so `apply --plan` keeps its
  drift check for a plan made from a model.
- The target schema must exist: like `--file`, planning against a missing schema fails with
  "schema '<name>' does not exist in the database".

## Build for the four RIDs

`mise x go@1.24 -- fork/build-rids.sh` cross-builds `bin/<rid>/pgschema` (`.exe` for win-x64) for linux-x64, linux-arm64, osx-arm64 and win-x64. merp packs them: its `mise run pack` puts them under `runtimes/<rid>/native/` of `Merp.Pgschema`, the one package that holds the client too, so the fork packs nothing itself.

## Rebase procedure

```sh
git fetch upstream --tags
git switch merp
git rebase vX.Y.Z                      # the new upstream release tag
mise x go@1.24 -- go test ./...        # full suite (embedded Postgres; a few minutes)
```

Then run erp's gate against the rebased build. On each rebase also read what moved under the
patch:

```sh
git diff vOLD vNEW -- cmd/plan/plan.go internal/postgres/desired_state.go ir/ir.go
```

- `GeneratePlan` in `cmd/plan/plan.go`: `generatePlanFromIR` (`cmd/plan/model.go`) mirrors its
  current-state half (ignore config, inspection, fingerprint, target version, diff, plan). A step
  upstream adds there must be mirrored.
- `DesiredStateProvider` (`internal/postgres/desired_state.go`): `model.Provider` implements it; a
  new method needs a no-op there.
- The IR shape (`ir/ir.go` JSON tags): the strict decode rejects documents that use a removed or
  renamed field; erp's deriver must follow the new shape.
- A new map field in the IR must be added to `fillMaps` (`internal/model/model.go`), which gives a
  model the non-nil maps the inspector always allocates.
- A new derived `json:"-"` field that the inspector sets outside `normalizeIR` is absent from a
  model IR (today `AllConstraintNames` and `PartitionParentColumns`; `IsSerial` is set by
  normalization). Check whether the differ reads it from the desired side.

## Rules

- New code goes in new files (a new package where it can). An upstream file gets at most a
  one-to-few-line hook: an import, a branch that selects the provider. Prefer added lines to
  modified ones. Each hook is marked `merp fork`.
- No refactors, renames, reformatting or comment edits in upstream files. No changes to upstream
  tests unless a hook forces one.
- Follow upstream's conventions in hook lines (package-level flag variables in `cmd/`); do not fix
  upstream style.
- One commit per logical step, hooks separate from new files where that helps a rebase.
- No upstream `.gitignore` edits: local worktrees under `.claude/worktrees/` stay untracked, and
  `bin/` is already covered by upstream's `pgschema` ignore pattern.
- Never push to a remote without being asked. `upstream` is fetch-only, enforced in each clone
  with `git remote set-url --push upstream DISABLE`.
