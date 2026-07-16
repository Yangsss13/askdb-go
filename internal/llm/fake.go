// Package llm provides SQL-generation clients. Stage 2 ships only a fake,
// deterministic client that maps a fixed set of demo questions to hardcoded SQL.
package llm

import (
	"context"
	"errors"
	"strings"
)

// ErrUnsupportedQuestion is returned when a question is not in the fixed set
// the fake client recognizes. Callers map this to a failed job / HTTP 422.
var ErrUnsupportedQuestion = errors.New("llm: unsupported question")

// FakeLLMClient is a deterministic stand-in for a real LLM. It does not call any
// model. It matches the trimmed question against a fixed table and returns the
// corresponding hardcoded SELECT. User input is never interpolated into SQL.
type FakeLLMClient struct {
	// fixedSQL maps an exact (trimmed) question to its hardcoded SQL.
	fixedSQL map[string]string
}

// NewFakeLLMClient returns a FakeLLMClient with the built-in demo questions.
//
// Every SQL statement is a read-only SELECT, lists columns explicitly (no
// SELECT *), and is capped with LIMIT 100.
func NewFakeLLMClient() *FakeLLMClient {
	return &FakeLLMClient{
		fixedSQL: map[string]string{
			"查询所有商品":    "SELECT id, name, category, price, stock, created_at FROM products ORDER BY id LIMIT 100",
			"查询销量最高的商品": "SELECT p.id, p.name, SUM(oi.quantity) AS total_sold FROM products p JOIN order_items oi ON oi.product_id = p.id GROUP BY p.id, p.name ORDER BY total_sold DESC LIMIT 100",
			"查询最近的订单":   "SELECT id, customer, status, total_amount, created_at FROM orders ORDER BY created_at DESC LIMIT 100",
		},
	}
}

// GenerateSQL returns the hardcoded SQL for a recognized question. The question
// is matched only after trimming leading/trailing whitespace; no fuzzy matching
// and no interpolation of user input into the returned SQL occurs.
// Unrecognized questions return ErrUnsupportedQuestion.
func (c *FakeLLMClient) GenerateSQL(_ context.Context, question string) (string, error) {
	sql, ok := c.fixedSQL[strings.TrimSpace(question)]
	if !ok {
		return "", ErrUnsupportedQuestion
	}
	return sql, nil
}
