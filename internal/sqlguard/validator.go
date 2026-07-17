package sqlguard

import (
	"sort"
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
)

// allowedFunctions is the conservative allowlist of SQL functions the Guard
// permits. Any function not in this set is rejected, which automatically blocks
// side-effecting or waiting functions (SLEEP, BENCHMARK, LOAD_FILE, GET_LOCK,
// RELEASE_LOCK, IS_FREE_LOCK, IS_USED_LOCK, MASTER_POS_WAIT,
// WAIT_FOR_EXECUTED_GTID_SET, and any unknown function). Names are lowercase.
var allowedFunctions = map[string]bool{
	// Aggregates
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	// Arithmetic / numeric
	"abs": true, "round": true, "ceil": true, "ceiling": true, "floor": true,
	"mod": true, "greatest": true, "least": true,
	// Conditional / null handling
	"coalesce": true, "ifnull": true, "nullif": true, "if": true,
	// String
	"length": true, "char_length": true, "character_length": true,
	"lower": true, "upper": true, "trim": true, "ltrim": true, "rtrim": true,
	"concat": true, "concat_ws": true, "substring": true, "substr": true,
	"left": true, "right": true, "replace": true,
	// Date/time extraction (read-only, deterministic on stored values)
	"year": true, "month": true, "day": true, "hour": true, "minute": true,
	"second": true, "date": true, "date_format": true,
}

// validator holds the whitelist configuration for one Validate call.
// A fresh validator is created per call; it holds no shared mutable state.
type validator struct {
	allowedTables  map[string]bool
	allowedSchemas map[string]bool
	// seenTables collects the distinct physical tables referenced (lowercased),
	// excluding CTE names. Used to populate ValidateResult.Tables.
	seenTables map[string]bool
}

// newValidator builds a validator from the allowed database and tables.
// The only allowed schema is allowedDatabase (askdb_demo); every other schema
// (askdb_app, mysql, information_schema, performance_schema, sys, ...) is rejected.
func newValidator(allowedDatabase string, allowedTables []string) *validator {
	tables := make(map[string]bool, len(allowedTables))
	for _, t := range allowedTables {
		tables[strings.ToLower(t)] = true
	}
	schemas := map[string]bool{}
	if allowedDatabase != "" {
		schemas[strings.ToLower(allowedDatabase)] = true
	}
	return &validator{
		allowedTables:  tables,
		allowedSchemas: schemas,
		seenTables:     map[string]bool{},
	}
}

// tableList returns the distinct physical tables referenced, sorted.
func (v *validator) tableList() []string {
	out := make([]string, 0, len(v.seenTables))
	for t := range v.seenTables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// check validates the root statement. Only a single SELECT or a set operation
// (UNION) may be the root; everything else is rejected.
func (v *validator) check(stmt ast.StmtNode) error {
	switch node := stmt.(type) {
	case *ast.SelectStmt:
		return v.checkSelect(node, nil)
	case *ast.SetOprStmt:
		return v.checkSetOpr(node, nil)
	default:
		return reject("only SELECT statements are allowed")
	}
}

// checkResultSet validates a nested result set (derived table, subquery body,
// CTE body, UNION branch) under the given CTE scope.
func (v *validator) checkResultSet(node ast.ResultSetNode, scope map[string]bool) error {
	switch n := node.(type) {
	case *ast.SelectStmt:
		return v.checkSelect(n, scope)
	case *ast.SetOprStmt:
		return v.checkSetOpr(n, scope)
	case *ast.Join:
		return v.checkJoinNode(n, scope)
	case *ast.TableSource:
		return v.checkTableSource(n, scope)
	default:
		return reject("unsupported query construct")
	}
}

// checkSelect validates a single SELECT. parentScope carries CTE names visible
// from enclosing WITH clauses (nil at the top level).
func (v *validator) checkSelect(sel *ast.SelectStmt, parentScope map[string]bool) error {
	// Reject SELECT ... INTO OUTFILE / DUMPFILE / @var.
	if sel.SelectIntoOpt != nil {
		return reject("SELECT ... INTO is not allowed")
	}
	// Reject FOR UPDATE / LOCK IN SHARE MODE.
	if sel.LockInfo != nil && sel.LockInfo.LockType != ast.SelectLockNone {
		return reject("row locking clauses are not allowed")
	}

	scope := parentScope
	if sel.With != nil {
		childScope, err := v.checkWith(sel.With, parentScope)
		if err != nil {
			return err
		}
		scope = childScope
	}

	if sel.From != nil && sel.From.TableRefs != nil {
		if err := v.checkJoinNode(sel.From.TableRefs, scope); err != nil {
			return err
		}
	}

	// Validate all expression clauses (functions, variables, nested subqueries).
	for _, expr := range selectExprNodes(sel) {
		if err := v.checkExpr(expr, scope); err != nil {
			return err
		}
	}
	return nil
}

// checkSetOpr validates a set operation (UNION / UNION ALL / etc.).
func (v *validator) checkSetOpr(setOpr *ast.SetOprStmt, parentScope map[string]bool) error {
	scope := parentScope
	if setOpr.With != nil {
		childScope, err := v.checkWith(setOpr.With, parentScope)
		if err != nil {
			return err
		}
		scope = childScope
	}
	if setOpr.SelectList == nil {
		return reject("empty set operation")
	}
	return v.checkSetOprSelectList(setOpr.SelectList, scope)
}

// checkSetOprSelectList recursively validates every branch of a set operation.
func (v *validator) checkSetOprSelectList(list *ast.SetOprSelectList, parentScope map[string]bool) error {
	scope := parentScope
	if list.With != nil {
		childScope, err := v.checkWith(list.With, parentScope)
		if err != nil {
			return err
		}
		scope = childScope
	}
	for _, branch := range list.Selects {
		switch b := branch.(type) {
		case *ast.SelectStmt:
			if err := v.checkSelect(b, scope); err != nil {
				return err
			}
		case *ast.SetOprSelectList:
			if err := v.checkSetOprSelectList(b, scope); err != nil {
				return err
			}
		default:
			return reject("unsupported set operation branch")
		}
	}
	return nil
}

// checkWith validates a WITH clause and returns the child scope (parent scope
// plus this clause's CTE names). WITH RECURSIVE is rejected. A CTE whose name
// shadows a physical whitelist table is rejected to avoid ambiguity.
func (v *validator) checkWith(with *ast.WithClause, parentScope map[string]bool) (map[string]bool, error) {
	if with.IsRecursive {
		return nil, reject("WITH RECURSIVE is not allowed")
	}

	childScope := make(map[string]bool, len(parentScope)+len(with.CTEs))
	for k := range parentScope {
		childScope[k] = true
	}
	for _, cte := range with.CTEs {
		name := cte.Name.L
		if v.allowedTables[name] {
			return nil, reject("CTE name shadows a physical table")
		}
		if cte.IsRecursive {
			return nil, reject("WITH RECURSIVE is not allowed")
		}
		childScope[name] = true
	}
	// Validate each CTE body against the scope that includes sibling CTEs.
	for _, cte := range with.CTEs {
		if cte.Query == nil || cte.Query.Query == nil {
			return nil, reject("invalid CTE definition")
		}
		if err := v.checkResultSet(cte.Query.Query, childScope); err != nil {
			return nil, err
		}
	}
	return childScope, nil
}

// checkJoinNode walks a FROM/JOIN tree, validating every table source and the
// ON conditions.
func (v *validator) checkJoinNode(node ast.ResultSetNode, scope map[string]bool) error {
	switch n := node.(type) {
	case *ast.Join:
		if n.Left != nil {
			if err := v.checkJoinNode(n.Left, scope); err != nil {
				return err
			}
		}
		if n.Right != nil {
			if err := v.checkJoinNode(n.Right, scope); err != nil {
				return err
			}
		}
		if n.On != nil {
			if err := v.checkExpr(n.On.Expr, scope); err != nil {
				return err
			}
		}
		return nil
	case *ast.TableSource:
		return v.checkTableSource(n, scope)
	case *ast.TableName:
		return v.checkTableName(n, scope)
	case *ast.SelectStmt:
		return v.checkSelect(n, scope)
	case *ast.SetOprStmt:
		return v.checkSetOpr(n, scope)
	default:
		return reject("unsupported table reference")
	}
}

// checkTableSource validates one table source (physical table, derived table,
// or nested join). Its alias is a label and needs no whitelist check.
func (v *validator) checkTableSource(ts *ast.TableSource, scope map[string]bool) error {
	switch src := ts.Source.(type) {
	case *ast.TableName:
		return v.checkTableName(src, scope)
	case *ast.SelectStmt:
		return v.checkSelect(src, scope)
	case *ast.SetOprStmt:
		return v.checkSetOpr(src, scope)
	case *ast.Join:
		return v.checkJoinNode(src, scope)
	default:
		return reject("unsupported table source")
	}
}

// checkTableName enforces the schema and table whitelist. An unqualified name
// that matches an in-scope CTE is allowed (its body was validated separately).
func (v *validator) checkTableName(tn *ast.TableName, scope map[string]bool) error {
	schema := tn.Schema.L
	name := tn.Name.L

	if schema == "" {
		if scope[name] {
			return nil // CTE reference in scope
		}
		if v.allowedTables[name] {
			v.seenTables[name] = true
			return nil
		}
		return reject("table is not allowed")
	}

	// Qualified name: schema must be allowed and table must be whitelisted.
	if !v.allowedSchemas[schema] {
		return reject("database is not allowed")
	}
	if !v.allowedTables[name] {
		return reject("table is not allowed")
	}
	v.seenTables[name] = true
	return nil
}

// checkExpr validates a single expression subtree via exprChecker.
func (v *validator) checkExpr(expr ast.Node, scope map[string]bool) error {
	if expr == nil {
		return nil
	}
	c := &exprChecker{v: v, scope: scope}
	expr.Accept(c)
	return c.err
}

// selectExprNodes returns the expression clause roots of a SELECT that must be
// validated (field list, WHERE, GROUP BY, HAVING, ORDER BY).
func selectExprNodes(sel *ast.SelectStmt) []ast.Node {
	var nodes []ast.Node
	if sel.Fields != nil {
		nodes = append(nodes, sel.Fields)
	}
	if sel.Where != nil {
		nodes = append(nodes, sel.Where)
	}
	if sel.GroupBy != nil {
		nodes = append(nodes, sel.GroupBy)
	}
	if sel.Having != nil {
		nodes = append(nodes, sel.Having)
	}
	if sel.OrderBy != nil {
		nodes = append(nodes, sel.OrderBy)
	}
	return nodes
}
