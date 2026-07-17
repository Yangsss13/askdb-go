package queryjob

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQueryJobMessage_MarshalUnmarshal(t *testing.T) {
	orig := QueryJobMessage{
		MessageID:  "abc123",
		Type:       MessageTypeQueryExecutionRequested,
		Version:    messageVersion,
		OccurredAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		Payload:    JobPayload{JobID: 42},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got QueryJobMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.MessageID != orig.MessageID {
		t.Errorf("message_id: got %q, want %q", got.MessageID, orig.MessageID)
	}
	if got.Type != orig.Type {
		t.Errorf("type: got %q, want %q", got.Type, orig.Type)
	}
	if got.Version != orig.Version {
		t.Errorf("version: got %d, want %d", got.Version, orig.Version)
	}
	if !got.OccurredAt.Equal(orig.OccurredAt) {
		t.Errorf("occurred_at: got %v, want %v", got.OccurredAt, orig.OccurredAt)
	}
	if got.Payload.JobID != orig.Payload.JobID {
		t.Errorf("payload.job_id: got %d, want %d", got.Payload.JobID, orig.Payload.JobID)
	}
}

func TestQueryJobMessage_NoSensitiveFields(t *testing.T) {
	msg := QueryJobMessage{
		MessageID:  "x",
		Type:       MessageTypeQueryExecutionRequested,
		Version:    messageVersion,
		OccurredAt: time.Now(),
		Payload:    JobPayload{JobID: 1},
	}
	data, _ := json.Marshal(msg)
	s := string(data)

	forbidden := []string{"question", "generated_sql", "dsn", "password", "token", "api_key"}
	for _, f := range forbidden {
		if contains(s, f) {
			t.Errorf("message JSON must not contain %q, got: %s", f, s)
		}
	}
}

func TestNewMessageID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := newMessageID()
		if id == "" {
			t.Fatal("newMessageID returned empty string")
		}
		if _, dup := ids[id]; dup {
			t.Fatalf("duplicate message_id: %q", id)
		}
		ids[id] = struct{}{}
	}
}

// contains is a simple case-insensitive substring check.
func contains(s, sub string) bool {
	sl, subl := len(s), len(sub)
	for i := 0; i <= sl-subl; i++ {
		match := true
		for j := 0; j < subl; j++ {
			c1, c2 := s[i+j], sub[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
