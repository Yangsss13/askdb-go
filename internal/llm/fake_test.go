package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeLLMClient_SupportedQuestions(t *testing.T) {
	c := NewFakeLLMClient()
	questions := []string{
		"查询所有商品",
		"查询销量最高的商品",
		"查询最近的订单",
	}

	for _, q := range questions {
		sql, err := c.GenerateSQL(context.Background(), q)
		if err != nil {
			t.Fatalf("question %q: unexpected error: %v", q, err)
		}
		upper := strings.ToUpper(sql)
		if !strings.HasPrefix(strings.TrimSpace(upper), "SELECT") {
			t.Errorf("question %q: SQL must be a SELECT, got %q", q, sql)
		}
		if strings.Contains(sql, "*") {
			t.Errorf("question %q: SQL must not use SELECT *, got %q", q, sql)
		}
		if !strings.Contains(upper, "LIMIT 100") {
			t.Errorf("question %q: SQL must contain LIMIT 100, got %q", q, sql)
		}
	}
}

func TestFakeLLMClient_TrimsWhitespace(t *testing.T) {
	c := NewFakeLLMClient()
	sql, err := c.GenerateSQL(context.Background(), "  查询所有商品  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sql == "" {
		t.Fatal("expected SQL for trimmed question")
	}
}

func TestFakeLLMClient_Unsupported(t *testing.T) {
	c := NewFakeLLMClient()
	unsupported := []string{
		"",
		"删除所有商品",
		"查询所有商品; DROP TABLE products",
		"random question",
	}
	for _, q := range unsupported {
		_, err := c.GenerateSQL(context.Background(), q)
		if !errors.Is(err, ErrUnsupportedQuestion) {
			t.Errorf("question %q: expected ErrUnsupportedQuestion, got %v", q, err)
		}
	}
}
