package sqlguard

import (
	"github.com/pingcap/tidb/pkg/parser/ast"
)

// rewriteLimit reads, validates, and rewrites the outermost LIMIT of the
// accepted statement so the effective row cap is at most maxRows. It returns the
// effective limit applied.
//
// Rules:
//   - No outermost LIMIT      → inject LIMIT maxRows.
//   - LIMIT count <= maxRows  → keep as-is.
//   - LIMIT count > maxRows   → compress count to maxRows.
//   - Non-literal count/offset (params, variables, expressions) → reject.
//   - Negative or non-integer count/offset → reject.
//   - OFFSET > maxRows        → reject.
//
// Only the outermost result layer is rewritten; inner CTEs, subqueries, and
// UNION branches are left untouched.
func rewriteLimit(stmt ast.StmtNode, maxRows int) (int, error) {
	limitPtr, err := outermostLimitPtr(stmt)
	if err != nil {
		return 0, err
	}

	// No LIMIT present: inject one.
	if *limitPtr == nil {
		*limitPtr = newLimit(maxRows)
		return maxRows, nil
	}

	limit := *limitPtr

	// Validate OFFSET if present (must be a non-negative integer literal <= maxRows).
	if limit.Offset != nil {
		offset, ok := literalUint(limit.Offset)
		if !ok {
			return 0, reject("unsupported LIMIT offset expression")
		}
		if offset > uint64(maxRows) {
			return 0, reject("LIMIT offset exceeds the maximum")
		}
	}

	// Count is required when a Limit node exists.
	if limit.Count == nil {
		return 0, reject("unsupported LIMIT expression")
	}
	count, ok := literalUint(limit.Count)
	if !ok {
		return 0, reject("unsupported LIMIT count expression")
	}

	if count > uint64(maxRows) {
		limit.Count = ast.NewValueExpr(uint64(maxRows), "", "")
		return maxRows, nil
	}
	return int(count), nil
}

// verifyOutermostLimit confirms that the (re-parsed) statement has an outermost
// LIMIT whose count is a literal in the range (0, maxRows]. Used as a
// post-rewrite invariant check on the normalized SQL.
func verifyOutermostLimit(stmt ast.StmtNode, maxRows int) error {
	limitPtr, err := outermostLimitPtr(stmt)
	if err != nil {
		return err
	}
	if *limitPtr == nil || (*limitPtr).Count == nil {
		return reject("normalized SQL is missing a LIMIT")
	}
	count, ok := literalUint((*limitPtr).Count)
	if !ok {
		return reject("normalized SQL has a non-literal LIMIT")
	}
	if count == 0 || count > uint64(maxRows) {
		return reject("normalized SQL LIMIT is out of range")
	}
	return nil
}

// outermostLimitPtr returns a pointer to the outermost *ast.Limit field so the
// caller can read or replace it. For a plain SELECT this is SelectStmt.Limit;
// for a UNION it is SetOprStmt.Limit (the set operation's own trailing LIMIT).
func outermostLimitPtr(stmt ast.StmtNode) (**ast.Limit, error) {
	switch n := stmt.(type) {
	case *ast.SelectStmt:
		return &n.Limit, nil
	case *ast.SetOprStmt:
		return &n.Limit, nil
	default:
		return nil, reject("unsupported statement for LIMIT rewrite")
	}
}

// newLimit builds a LIMIT <count> node from an integer.
func newLimit(count int) *ast.Limit {
	return &ast.Limit{Count: ast.NewValueExpr(uint64(count), "", "")}
}

// literalUint reads a non-negative integer literal from an expression node.
// Returns ok=false for any non-literal expression (parameter markers, function
// calls, arithmetic, variables) or a negative/non-integer value.
func literalUint(expr ast.ExprNode) (uint64, bool) {
	ve, ok := expr.(ast.ValueExpr)
	if !ok {
		return 0, false
	}
	switch val := ve.GetValue().(type) {
	case uint64:
		return val, true
	case int64:
		if val < 0 {
			return 0, false
		}
		return uint64(val), true
	default:
		return 0, false
	}
}
