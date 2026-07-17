package sqlguard

import (
	"context"
	"fmt"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"

	// test_driver registers the value-expression driver used to read literal
	// values (e.g. LIMIT counts) from the AST. Despite its name it imports only
	// pkg/parser subpackages and pulls in no heavy TiDB dependencies.
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// ValidateInput carries the SQL to validate and the policy it is validated against.
type ValidateInput struct {
	// SQL is the raw statement produced upstream (untrusted).
	SQL string
	// AllowedDatabase is the only schema qualifier permitted on table names.
	// An empty (unqualified) table reference is also allowed.
	AllowedDatabase string
	// AllowedTables is the set of physical table names that may be referenced.
	AllowedTables []string
	// MaxRows is the maximum permitted outermost LIMIT. Must be greater than zero.
	MaxRows int
}

// ValidateResult is the outcome of a successful validation.
type ValidateResult struct {
	// NormalizedSQL is the rewritten, safe-to-execute SQL. Callers MUST execute
	// this string, never the original input.
	NormalizedSQL string
	// StatementType is a stable label for the accepted statement ("SELECT" or "UNION").
	StatementType string
	// Tables are the distinct physical tables referenced, lowercased.
	Tables []string
	// Limit is the effective outermost LIMIT applied to NormalizedSQL.
	Limit int
}

// Guard validates SQL against a fixed policy. It is stateless and safe for
// concurrent use; each Validate call uses its own parser instance.
type Guard struct{}

// New returns a ready-to-use Guard.
func New() *Guard { return &Guard{} }

// Validate parses input.SQL, enforces the allowlist rules, rewrites the
// outermost LIMIT, and re-parses the rewritten SQL to confirm the key
// invariants still hold.
//
// Return semantics:
//   - (result, nil)         → SQL is safe; execute result.NormalizedSQL.
//   - (zero, ErrRejected)   → deterministic rejection; wrapped reason is for
//     internal logging only. Caller marks the job failed and ACKs.
//   - (zero, ctx.Err())     → context cancelled/expired; a runtime error, not a
//     security rejection. Caller applies normal ACK/NACK semantics.
//
// A parse failure is always treated as a rejection (fail-closed): SQL that the
// MySQL-dialect parser cannot understand is never allowed to execute.
func (g *Guard) Validate(ctx context.Context, input ValidateInput) (ValidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ValidateResult{}, err
	}
	if input.MaxRows <= 0 {
		return ValidateResult{}, fmt.Errorf("%w: invalid MaxRows", ErrRejected)
	}

	p := parser.New()

	stmt, err := parseSingle(p, input.SQL)
	if err != nil {
		return ValidateResult{}, err // already wrapped as ErrRejected
	}

	// Walk the AST and enforce every structural rule (tables, schemas, functions,
	// variables, INTO/lock clauses, nested CTE/subquery/UNION scoping).
	v := newValidator(input.AllowedDatabase, input.AllowedTables)
	if err := v.check(stmt); err != nil {
		return ValidateResult{}, err
	}

	// Rewrite the outermost LIMIT on the accepted statement.
	limit, err := rewriteLimit(stmt, input.MaxRows)
	if err != nil {
		return ValidateResult{}, err
	}

	normalized, err := restoreSQL(stmt)
	if err != nil {
		return ValidateResult{}, err
	}

	// Re-parse the rewritten SQL and re-check the invariants. This guarantees the
	// output we hand to the executor is itself a single, safe statement.
	stmt2, err := parseSingle(p, normalized)
	if err != nil {
		return ValidateResult{}, err
	}
	v2 := newValidator(input.AllowedDatabase, input.AllowedTables)
	if err := v2.check(stmt2); err != nil {
		return ValidateResult{}, err
	}
	if err := verifyOutermostLimit(stmt2, input.MaxRows); err != nil {
		return ValidateResult{}, err
	}

	return ValidateResult{
		NormalizedSQL: normalized,
		StatementType: statementType(stmt),
		Tables:        v.tableList(),
		Limit:         limit,
	}, nil
}

// statementType returns a stable label for the accepted root node.
func statementType(stmt ast.StmtNode) string {
	switch stmt.(type) {
	case *ast.SetOprStmt:
		return "UNION"
	default:
		return "SELECT"
	}
}
