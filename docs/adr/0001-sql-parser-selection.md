# ADR 0001: SQL Parser Selection for SQL Guard

## Status

Accepted (Phase 5)

## Context

AskDB-Go Phase 5 introduces a SQL Guard in the Worker pipeline. Before executing any SQL produced by the LLM, the Guard must:

- Parse the SQL into an AST.
- Enforce a strict allowlist: only read-only SELECT statements, over a fixed set of tables, with a bounded outermost LIMIT.
- Rewrite the outermost LIMIT via AST mutation (not string concatenation).
- Detect multi-statement input, disallowed statement types, banned functions, variables, and schema/table violations.
- Recursively validate CTEs, subqueries, and UNION branches with correct scope tracking.

Using only regex or string-prefix matching is explicitly out of scope: it is bypassable through comments, case variations, and encoding tricks.

## Candidates Evaluated

### Option A: `github.com/pingcap/tidb/pkg/parser` (chosen)

The TiDB SQL parser extracted as a standalone Go module (`pkg/parser`) with its own `go.mod` since mid-2024. Provides the same parser that powers TiDB's MySQL-8.0-compatible database.

**MySQL dialect:** Full MySQL 8.0 support. WITH/CTE (including `IsRecursive` flag), UNION, correlated subqueries, window functions, JSON operators — all represented in the AST.

**AST Visitor:** Double-phase `Enter/Leave` Visitor interface on every `ast.Node`. Recursive traversal is straightforward. `SelectStmt.With`, `.From`, `.SelectIntoOpt`, `.LockInfo`, `.Fields`, `.Where`, `.GroupBy`, `.Having`, `.OrderBy` are all first-class fields.

**LIMIT read/rewrite:** `SelectStmt.Limit` is a `*ast.Limit{Count, Offset ExprNode}`. Integer literals come through as `ast.ValueExpr` (via the bundled `test_driver`). Rewriting is: set `stmt.Limit = &ast.Limit{Count: ast.NewValueExpr(uint64(n), "", "")}`. The `format.RestoreCtx` API round-trips the modified AST back to valid SQL.

**Multi-statement detection:** `p.Parse(sql, "", "")` returns `([]ast.StmtNode, warns, err)`. `len(stmts) != 1` → reject.

**Function extraction:** `ast.FuncCallExpr.FnName.L` (lowercased), `ast.AggregateFuncExpr.F` (lowercased), `ast.WindowFuncExpr.Name`.

**Table/schema extraction:** `ast.TableName.Name.L` and `ast.TableName.Schema.L` (both carry lowercase and original-case variants via `ast.CIStr`).

**Maintenance:** Active. The `pkg/parser` module tracks TiDB releases (v8.x, 2025–2026). Last verified active: July 2026.

**License:** Apache 2.0.

**Dependency footprint:** 8 indirect dependencies (pingcap/errors, pingcap/failpoint, pingcap/log, go-semver, zap, atomic, multierr, lumberjack). No etcd, no gRPC, no Prometheus, no TiKV client. Verified by running `go mod tidy` in isolation.

**Value-expression driver:** The bundled `pkg/parser/test_driver` package (despite its name) is the standalone value-expression implementation. It imports only `pkg/parser/*` subpackages. No `pkg/types` or other heavy TiDB packages are pulled in.

**Go version:** Compatible with Go 1.21+; project uses Go 1.26.

**Verified behaviours (from probe program and unit tests):**
- LIMIT literals (`LIMIT 10`, `LIMIT 5, 20`, `LIMIT 20 OFFSET 5`) read correctly.
- `LIMIT ?` (parameter marker) → not a `ValueExpr` → rejected.
- `LIMIT 2+3`, `LIMIT -1` → parse error → rejected.
- LIMIT injection (`&ast.Limit{Count: NewValueExpr(100,...)}`) + Restore round-trips cleanly.
- `SelectIntoOpt != nil` → INTO OUTFILE/DUMPFILE/vars detected.
- `LockInfo.LockType != SelectLockNone` → FOR UPDATE/LOCK IN SHARE MODE detected.
- `WithClause.IsRecursive` → WITH RECURSIVE detected.
- `VariableExpr` → user/session/system variable detected.

### Option B: `github.com/xwb1989/sqlparser`

A snapshot of Vitess's SQL parser from ~2018. No WITH/CTE support (the grammar predates MySQL 8.0 CTEs). Abandoned: last substantive commit 2021. The absence of CTE support alone disqualifies it, since Phase 5 requires recursive validation of CTE bodies.

### Option C: `github.com/blastrain/vitess-sqlparser`

Embeds both a Vitess snapshot and an old TiDB snapshot, both pre-CTE. Abandoned since ~2020. Same disqualification as Option B, with additional confusion from the dual-backend design.

## Decision

**Use `github.com/pingcap/tidb/pkg/parser` with `pkg/parser/test_driver`.**

The module satisfies every stated requirement:
- Full MySQL 8.0 AST including CTEs.
- Double-phase Visitor for recursive, scope-aware traversal.
- AST-based LIMIT rewrite (no string concatenation).
- Multi-statement detection via slice length.
- All security-relevant fields (`SelectIntoOpt`, `LockInfo`, `WithClause.IsRecursive`, `VariableExpr`, `FuncCallExpr`) are reachable as typed struct fields.
- Lean dependency footprint (8 indirects, all Apache 2.0).
- Actively maintained.

The older `github.com/pingcap/parser` standalone archive was considered as an alternative but is archived (last commit December 2023) and requires a `pingcap/tidb/types/parser_driver` shim anyway. The active `pkg/parser` module is strictly better and was verified to have an equivalently lean footprint.

## Fail-Closed Policy

A parse error is treated as a rejection, not a fallback to execution. SQL that the MySQL-dialect parser cannot understand is never executed. This applies even when the failure is a TiDB-specific MySQL extension gap.

## Known Limitations

- **MySQL 8.4+ syntax gaps:** If TiDB's parser has not yet implemented a MySQL 8.4+ feature, any SQL using that feature will parse-fail and be rejected. This is the safe direction.
- **`test_driver` naming:** The value-expression driver is named `test_driver` for historical reasons. It is production-safe and imports only sibling `pkg/parser` packages.
- **Guard is defense-in-depth:** The SQL Guard does not replace the `askdb_reader` database account, which has SELECT-only access to `askdb_demo`. Both controls must be present.
- **Comments:** Block and line comments are stripped by the parser's tokenizer before AST construction. A comment cannot introduce a new statement or bypass a table check.
- **WITH RECURSIVE:** Rejected by the Guard in Phase 5. May be reconsidered in a future phase with explicit policy.
