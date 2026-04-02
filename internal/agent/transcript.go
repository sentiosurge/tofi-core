package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tofi-core/internal/provider"
)

// Transcript persists agent loop state to disk for crash recovery.
// Before each API call, the current state is written as a checkpoint.
// If the process crashes, the transcript can be used to resume.
type Transcript struct {
	mu       sync.Mutex
	filePath string
	entries  []TranscriptEntry
}

// TranscriptEntry represents a single checkpoint in the agent loop.
type TranscriptEntry struct {
	Step      int              `json:"step"`
	Phase     string           `json:"phase"`
	Timestamp time.Time        `json:"timestamp"`
	Messages  []TranscriptMsg  `json:"messages"`
	Usage     provider.Usage   `json:"usage"`
	LLMCalls  int              `json:"llm_calls"`
}

// TranscriptMsg is a lightweight message representation for persistence.
type TranscriptMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	HasCalls   bool   `json:"has_calls,omitempty"` // true if assistant message has tool calls
}

// NewTranscript creates a transcript writer for the given session.
// If userDir is provided, files are stored in {userDir}/transcripts/{sessionID}.jsonl
// (per-user isolation). Otherwise falls back to ~/.tofi/transcripts/{sessionID}.jsonl.
func NewTranscript(sessionID, userDir string) (*Transcript, error) {
	var dir string
	if userDir != "" {
		dir = filepath.Join(userDir, "transcripts")
	} else {
		dir = filepath.Join(os.Getenv("HOME"), ".tofi", "transcripts")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}

	return &Transcript{
		filePath: filepath.Join(dir, sessionID+".jsonl"),
	}, nil
}

// Checkpoint writes the current agent state to disk before an API call.
// This is the "WAL" (write-ahead log) that enables crash recovery.
func (t *Transcript) Checkpoint(step int, phase AgentPhase, messages []provider.Message, usage provider.Usage, llmCalls int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry := TranscriptEntry{
		Step:      step,
		Phase:     phase.String(),
		Timestamp: time.Now(),
		Messages:  convertToTranscriptMsgs(messages),
		Usage:     usage,
		LLMCalls:  llmCalls,
	}

	t.entries = append(t.entries, entry)

	// Append to JSONL file (one JSON object per line)
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal transcript entry: %w", err)
	}

	f, err := os.OpenFile(t.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open transcript file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write transcript entry: %w", err)
	}

	return nil
}

// LoadLastCheckpoint reads the most recent checkpoint from disk.
// Returns nil if no transcript exists.
func LoadLastCheckpoint(sessionID, userDir string) (*TranscriptEntry, error) {
	var dir string
	if userDir != "" {
		dir = filepath.Join(userDir, "transcripts")
	} else {
		dir = filepath.Join(os.Getenv("HOME"), ".tofi", "transcripts")
	}
	path := filepath.Join(dir, sessionID+".jsonl")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	// Find the last non-empty line
	lines := splitLines(data)
	if len(lines) == 0 {
		return nil, nil
	}

	lastLine := lines[len(lines)-1]
	var entry TranscriptEntry
	if err := json.Unmarshal(lastLine, &entry); err != nil {
		return nil, fmt.Errorf("parse last checkpoint: %w", err)
	}

	return &entry, nil
}

// Clean removes the transcript file after successful completion.
func (t *Transcript) Clean() error {
	return os.Remove(t.filePath)
}

// Path returns the file path of the transcript.
func (t *Transcript) Path() string {
	return t.filePath
}

func convertToTranscriptMsgs(messages []provider.Message) []TranscriptMsg {
	result := make([]TranscriptMsg, len(messages))
	for i, msg := range messages {
		tm := TranscriptMsg{
			Role:       msg.Role,
			ToolCallID: msg.ToolCallID,
			ToolName:   msg.ToolName,
			HasCalls:   len(msg.ToolCalls) > 0,
		}
		// Truncate content for checkpoint — full content is in session XML
		if len(msg.Content) > 500 {
			tm.Content = msg.Content[:500] + "...[truncated]"
		} else {
			tm.Content = msg.Content
		}
		result[i] = tm
	}
	return result
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	// Last line without trailing newline
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
