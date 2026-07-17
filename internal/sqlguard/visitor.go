package sqlguard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// exprChecker is an ast.Visitor that validates expression subtrees. It rejects
// variables and disallowed functions, and recursively validates subqueries
// against the current CTE scope. It stops walking as soon as it records an error.
type exprChecker struct {
	v     *validator
	scope map[string]bool
	err   error
}

// Enter inspects each node. Returning skipChildren=true prevents the default
// walk from descending, which we use for subqueries (validated recursively with
// scope) and after recording an error.
func (c *exprChecker) Enter(n ast.Node) (ast.Node, bool) {
	if c.err != nil {
		return n, true
	}

	switch node := n.(type) {
	case *ast.VariableExpr:
		// User variables (@x), session/system variables (@@var).
		c.err = reject("variables are not allowed")
		return n, true

	case ast.ParamMarkerExpr:
		// Placeholder markers must never appear in generated SQL.
		c.err = reject("parameter markers are not allowed")
		return n, true

	case *ast.FuncCallExpr:
		if !allowedFunctions[node.FnName.L] {
			c.err = reject("function is not allowed")
			return n, true
		}

	case *ast.AggregateFuncExpr:
		if !allowedFunctions[strings.ToLower(node.F)] {
			c.err = reject("function is not allowed")
			return n, true
		}

	case *ast.WindowFuncExpr:
		// Window functions are out of scope for this phase.
		c.err = reject("window functions are not allowed")
		return n, true

	case *ast.SubqueryExpr:
		// Validate the subquery body with the current scope, then skip children
		// so the default walk does not re-descend with the wrong scope.
		if node.Query != nil {
			if err := c.v.checkResultSet(node.Query, c.scope); err != nil {
				c.err = err
			}
		}
		return n, true
	}

	return n, false
}

// Leave is a no-op; all decisions are made in Enter.
func (c *exprChecker) Leave(n ast.Node) (ast.Node, bool) {
	return n, c.err == nil
}
