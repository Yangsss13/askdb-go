package sqlguard

import (
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
)

// parseSingle parses sql and returns the single statement it contains.
// It rejects (fail-closed) on parse errors, empty input, and multi-statement
// input. Only SELECT and UNION/set-operation statements are accepted as roots.
func parseSingle(p *parser.Parser, sql string) (ast.StmtNode, error) {
	if strings.TrimSpace(sql) == "" {
		return nil, fmt.Errorf("%w: empty statement", ErrRejected)
	}

	stmts, _, err := p.Parse(sql, "", "")
	if err != nil {
		// Do not surface parser internals to callers; wrap as a rejection.
		return nil, fmt.Errorf("%w: parse error", ErrRejected)
	}
	if len(stmts) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one statement, got %d", ErrRejected, len(stmts))
	}

	switch stmts[0].(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
		return stmts[0], nil
	default:
		return nil, fmt.Errorf("%w: root node is not a SELECT", ErrRejected)
	}
}

// restoreSQL serializes a validated AST back into SQL text. It uses parser
// escaping (never string concatenation) so identifiers and literals are
// rendered safely.
func restoreSQL(stmt ast.StmtNode) (string, error) {
	var sb strings.Builder
	flags := format.DefaultRestoreFlags | format.RestoreStringSingleQuotes
	rctx := format.NewRestoreCtx(flags, &sb)
	if err := stmt.Restore(rctx); err != nil {
		return "", fmt.Errorf("%w: restore failed", ErrRejected)
	}
	return sb.String(), nil
}
