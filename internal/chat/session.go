// Package chat provides session-based chat management with XML file persistence
// and SQLite indexing. Sessions can be scoped to a user (main chat) or an agent
// (agent workspace chat).
package chat

import (
	"encoding/xml"
	"fmt"
	"time"
)

// Scope constants for session ownership.
const (
	ScopeUser = ""     // User's main chat (no prefix)
	ScopeAgentPrefix = "agent:" // Agent workspace chat prefix
)

// AgentScope returns the scope string for an agent.
func AgentScope(agentName string) string {
	return ScopeAgentPrefix + agentName
}

// Session represents a chat conversation persisted as an XML file.
type Session struct {
	XMLName xml.Name `xml:"session"`
	ID      string   `xml:"id,attr"`
	Title   string   `xml:"title,attr,omitempty"`
	Model   string   `xml:"model,attr,omitempty"`
	Skills  string   `xml:"skills,attr,omitempty"` // comma-separated skill names
	Created string   `xml:"created,attr"`
	Updated string   `xml:"updated,attr"`

	Summary  string    `xml:"summary,omitempty"`
	Usage    Usage     `xml:"usage"`
	Messages []Message `xml:"messages>msg"`
}

// Usage tracks cumulative token usage and cost for a session.
type Usage struct {
	XMLName      xml.Name `xml:"usage"`
	InputTokens  int64    `xml:"input_tokens,attr"`
	OutputTokens int64    `xml:"output_tokens,attr"`
	Cost         float64  `xml:"cost,attr"`
}

// Message represents a single message in a chat session.
type Message struct {
	XMLName    xml.Name    `xml:"msg"`
	Role       string      `xml:"role,attr"`
	Timestamp  string      `xml:"ts,attr,omitempty"`
	Tokens     int         `xml:"tokens,attr,omitempty"`
	CallID     string      `xml:"call_id,attr,omitempty"` // for tool response messages
	Name       string      `xml:"name,attr,omitempty"`    // tool name for tool responses
	Content    string      `xml:",chardata"`
	ToolCalls  []ToolCall  `xml:"tool_calls>call,omitempty"`
}

// ToolCall represents an LLM-initiated tool invocation within an assistant message.
type ToolCall struct {
	XMLName xml.Name `xml:"call"`
	ID      string   `xml:"id,attr"`
	Name    string   `xml:"name,attr"`
	Input   string   `xml:"input"`
}

// NewSession creates a new empty session with a generated ID.
func NewSession(id, model, skills string) *Session {
	now := time.Now().UTC().Format(time.RFC3339)
	return &Session{
		ID:      id,
		Model:   model,
		Skills:  skills,
		Created: now,
		Updated: now,
	}
}

// AddMessage appends a message to the session and updates the timestamp.
func (s *Session) AddMessage(msg Message) {
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	s.Messages = append(s.Messages, msg)
	s.Updated = time.Now().UTC().Format(time.RFC3339)
}

// MessageCount returns the number of messages in the session.
func (s *Session) MessageCount() int {
	return len(s.Messages)
}

// MarshalXML produces the XML representation of a session.
func (s *Session) Marshal() ([]byte, error) {
	data, err := xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal session XML: %w", err)
	}
	return append([]byte(xml.Header), data...), nil
}

// UnmarshalSession parses XML data into a Session.
func UnmarshalSession(data []byte) (*Session, error) {
	var s Session
	if err := xml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session XML: %w", err)
	}
	return &s, nil
}
