package agent

import (
	"os"
	"testing"

	"tofi-core/internal/provider"
)

func TestTranscript_CheckpointAndLoad(t *testing.T) {
	sessionID := "test_transcript_" + t.Name()

	tr, err := NewTranscript(sessionID, "")
	if err != nil {
		t.Fatalf("NewTranscript failed: %v", err)
	}
	defer os.Remove(tr.Path())

	// Write checkpoint
	messages := []provider.Message{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi there, how can I help?"},
	}
	err = tr.Checkpoint(1, PhaseThinking, messages, provider.Usage{InputTokens: 100, OutputTokens: 50}, 1)
	if err != nil {
		t.Fatalf("Checkpoint failed: %v", err)
	}

	// Write second checkpoint
	messages = append(messages, provider.Message{Role: "user", Content: "run a command"})
	err = tr.Checkpoint(2, PhaseExecuting, messages, provider.Usage{InputTokens: 200, OutputTokens: 100}, 2)
	if err != nil {
		t.Fatalf("Checkpoint 2 failed: %v", err)
	}

	// Load last checkpoint
	entry, err := LoadLastCheckpoint(sessionID, "")
	if err != nil {
		t.Fatalf("LoadLastCheckpoint failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if entry.Step != 2 {
		t.Errorf("expected step 2, got %d", entry.Step)
	}
	if entry.Phase != "executing" {
		t.Errorf("expected phase 'executing', got %q", entry.Phase)
	}
	if len(entry.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(entry.Messages))
	}
	if entry.LLMCalls != 2 {
		t.Errorf("expected 2 llm calls, got %d", entry.LLMCalls)
	}
}

func TestTranscript_LoadNonExistent(t *testing.T) {
	entry, err := LoadLastCheckpoint("nonexistent_session_xyz", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for nonexistent session")
	}
}

func TestTranscript_ContentTruncation(t *testing.T) {
	sessionID := "test_truncation_" + t.Name()
	tr, err := NewTranscript(sessionID, "")
	if err != nil {
		t.Fatalf("NewTranscript failed: %v", err)
	}
	defer os.Remove(tr.Path())

	// Long content should be truncated in checkpoint
	longContent := ""
	for i := 0; i < 200; i++ {
		longContent += "This is a very long line of content. "
	}

	messages := []provider.Message{
		{Role: "tool", Content: longContent},
	}
	err = tr.Checkpoint(1, PhaseThinking, messages, provider.Usage{}, 0)
	if err != nil {
		t.Fatalf("Checkpoint failed: %v", err)
	}

	entry, err := LoadLastCheckpoint(sessionID, "")
	if err != nil {
		t.Fatalf("LoadLastCheckpoint failed: %v", err)
	}

	// Checkpoint should have truncated the content
	if len(entry.Messages[0].Content) >= len(longContent) {
		t.Error("checkpoint should truncate long content")
	}
	if len(entry.Messages[0].Content) > 600 {
		t.Errorf("truncated content too long: %d chars", len(entry.Messages[0].Content))
	}
}

func TestTranscript_Clean(t *testing.T) {
	sessionID := "test_clean_" + t.Name()
	tr, err := NewTranscript(sessionID, "")
	if err != nil {
		t.Fatalf("NewTranscript failed: %v", err)
	}

	tr.Checkpoint(1, PhaseThinking, nil, provider.Usage{}, 0)

	// File should exist
	if _, err := os.Stat(tr.Path()); os.IsNotExist(err) {
		t.Fatal("transcript file should exist after checkpoint")
	}

	// Clean should remove it
	tr.Clean()
	if _, err := os.Stat(tr.Path()); !os.IsNotExist(err) {
		t.Fatal("transcript file should be removed after clean")
	}
}
