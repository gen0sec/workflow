package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateExecutionID_Accepts(t *testing.T) {
	for _, id := range []string{
		"exec_aaaaaaaaaaaaaaaaaaaaaaaaaa", // engine NewExecutionID format
		"ui-1748524895123456789",          // workflow-service UI run key
		"monitoring-1m-1234567890",
		"abc.def_ghi-123",
		"X", // 1 char ok
		strings.Repeat("a", 128), // exactly at length cap
	} {
		if err := validateExecutionID(id); err != nil {
			t.Errorf("valid id %q rejected: %v", id, err)
		}
	}
}

func TestValidateExecutionID_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"too long":          strings.Repeat("a", 129),
		"parent traversal":  "..",
		"sneaky traversal":  "../etc/passwd",
		"slash":             "a/b",
		"backslash":         "a\\b",
		"null byte":         "a\x00b",
		"space":             "a b",
		"colon":             "a:b",
		"absolute path":     "/etc/passwd",
		"home expansion":    "~root",
		"unicode":           "exec_中文",
	}
	for name, id := range cases {
		if err := validateExecutionID(id); err == nil {
			t.Errorf("%s: %q must be rejected", name, id)
		}
	}
}

// TestFileCheckpointer_RejectsTraversal is the end-to-end regression
// guard for security review M8: SaveCheckpoint must refuse an
// executionID that escapes the data directory, and must NOT create a
// file outside it. filepath.Join does not clean ".." segments — only
// the explicit validator stops the escape.
func TestFileCheckpointer_RejectsTraversal(t *testing.T) {
	tempDir := t.TempDir()
	c, err := NewFileCheckpointer(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	cp := &Checkpoint{
		ExecutionID:   "../escaped",
		ID:            "ck1",
		SchemaVersion: CheckpointSchemaVersion,
		WorkflowName:  "w",
		Status:        ExecutionStatusRunning,
		StartTime:     time.Now(),
		CheckpointAt:  time.Now(),
	}
	if err := c.SaveCheckpoint(context.Background(), cp); err == nil {
		t.Fatal("SaveCheckpoint must reject traversal id")
	}

	// No file may have been created at the escaped location.
	escaped := filepath.Join(tempDir, "..", "escaped")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("unexpected file created at %s — traversal succeeded", escaped)
		_ = os.RemoveAll(escaped) // cleanup if we somehow got here
	}

	if _, err := c.LoadCheckpoint(context.Background(), "../escaped"); err == nil {
		t.Fatal("LoadCheckpoint must reject traversal id")
	}
	if err := c.DeleteCheckpoint(context.Background(), "../escaped"); err == nil {
		t.Fatal("DeleteCheckpoint must reject traversal id")
	}
}

// TestFileActivityLogger_RejectsTraversal is the matching guard for
// the activity-log writer.
func TestFileActivityLogger_RejectsTraversal(t *testing.T) {
	tempDir := t.TempDir()
	l := NewFileActivityLogger(tempDir)

	entry := &ActivityLogEntry{ExecutionID: "../escaped", Activity: "x"}
	if err := l.LogActivity(context.Background(), entry); err == nil {
		t.Fatal("LogActivity must reject traversal id")
	}
	escaped := filepath.Join(tempDir, "..", "escaped.jsonl")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("unexpected file created at %s — traversal succeeded", escaped)
		_ = os.Remove(escaped)
	}
	if _, err := l.GetActivityHistory(context.Background(), "../escaped"); err == nil {
		t.Fatal("GetActivityHistory must reject traversal id")
	}
}
