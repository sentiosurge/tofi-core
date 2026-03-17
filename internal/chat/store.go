package chat

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tofi-core/internal/storage"
)

// Store manages chat sessions as XML files with a SQLite index for fast lookups.
// XML files are the source of truth; the SQLite index is a cache that can be rebuilt.
type Store struct {
	homeDir string
	db      *storage.DB
}

// NewStore creates a new chat store.
func NewStore(homeDir string, db *storage.DB) *Store {
	return &Store{homeDir: homeDir, db: db}
}

// sessionDir returns the directory for sessions of a given scope under a user.
func (s *Store) sessionDir(userID, scope string) string {
	if strings.HasPrefix(scope, ScopeAgentPrefix) {
		agentName := strings.TrimPrefix(scope, ScopeAgentPrefix)
		dir := filepath.Join(s.homeDir, "users", userID, "agents", agentName, "chat")
		os.MkdirAll(dir, 0755)
		return dir
	}
	// User main chat
	dir := filepath.Join(s.homeDir, "users", userID, "chat")
	os.MkdirAll(dir, 0755)
	return dir
}

// sessionFilePath returns the absolute path for a session XML file.
func (s *Store) sessionFilePath(userID, scope, sessionID string) string {
	return filepath.Join(s.sessionDir(userID, scope), sessionID+".xml")
}

// relPath returns the path relative to homeDir.
func (s *Store) relPath(absPath string) string {
	rel, err := filepath.Rel(s.homeDir, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// Save writes a session to an XML file and updates the SQLite index.
func (s *Store) Save(userID, scope string, session *Session) error {
	absPath := s.sessionFilePath(userID, scope, session.ID)

	data, err := session.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(absPath, data, 0644); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	// Update SQLite index
	idx := &storage.ChatSessionIndex{
		ID:                session.ID,
		UserID:            userID,
		Scope:             scope,
		Title:             session.Title,
		Model:             session.Model,
		MessageCount:      session.MessageCount(),
		TotalInputTokens:  session.Usage.InputTokens,
		TotalOutputTokens: session.Usage.OutputTokens,
		TotalCost:         session.Usage.Cost,
		FilePath:          s.relPath(absPath),
		CreatedAt:         session.Created,
		UpdatedAt:         session.Updated,
	}
	if err := s.db.UpsertChatSessionIndex(idx); err != nil {
		log.Printf("⚠️  failed to update chat session index: %v", err)
		// Non-fatal: file is the source of truth
	}
	return nil
}

// Load reads a session from its XML file.
func (s *Store) Load(userID, scope, sessionID string) (*Session, error) {
	absPath := s.sessionFilePath(userID, scope, sessionID)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	return UnmarshalSession(data)
}

// LoadByID loads a session using the SQLite index to find its file path.
func (s *Store) LoadByID(sessionID string) (*Session, error) {
	idx, err := s.db.GetChatSessionIndex(sessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	absPath := filepath.Join(s.homeDir, idx.FilePath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	return UnmarshalSession(data)
}

// List returns session index records from SQLite for fast listing.
func (s *Store) List(userID, scope string, limit int) ([]*storage.ChatSessionIndex, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.db.ListChatSessions(userID, scope, limit)
}

// Delete removes both the XML file and the SQLite index record.
func (s *Store) Delete(userID, scope, sessionID string) error {
	absPath := s.sessionFilePath(userID, scope, sessionID)
	os.Remove(absPath) // ignore error if file doesn't exist

	return s.db.DeleteChatSessionIndex(sessionID)
}

// GetIndex returns the SQLite index record for a session.
func (s *Store) GetIndex(sessionID string) (*storage.ChatSessionIndex, error) {
	return s.db.GetChatSessionIndex(sessionID)
}

// RebuildIndex scans the workspace for XML files and rebuilds the SQLite index.
// This handles manual edits, file moves, or DB corruption.
func (s *Store) RebuildIndex(userID string) (int, error) {
	count := 0

	// Scan user main chat
	if err := s.rebuildDir(userID, ScopeUser, &count); err != nil {
		log.Printf("⚠️  rebuild user chat index: %v", err)
	}

	// Scan agent chat directories
	agentsDir := filepath.Join(s.homeDir, "users", userID, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return count, err
		}
		return count, nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scope := AgentScope(entry.Name())
		if err := s.rebuildDir(userID, scope, &count); err != nil {
			log.Printf("⚠️  rebuild agent %s chat index: %v", entry.Name(), err)
		}
	}

	return count, nil
}

func (s *Store) rebuildDir(userID, scope string, count *int) error {
	dir := s.sessionDir(userID, scope)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".xml") {
			continue
		}

		absPath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(absPath)
		if err != nil {
			log.Printf("⚠️  read %s: %v", absPath, err)
			continue
		}
		session, err := UnmarshalSession(data)
		if err != nil {
			log.Printf("⚠️  parse %s: %v", absPath, err)
			continue
		}

		idx := &storage.ChatSessionIndex{
			ID:                session.ID,
			UserID:            userID,
			Scope:             scope,
			Title:             session.Title,
			Model:             session.Model,
			MessageCount:      session.MessageCount(),
			TotalInputTokens:  session.Usage.InputTokens,
			TotalOutputTokens: session.Usage.OutputTokens,
			TotalCost:         session.Usage.Cost,
			FilePath:          s.relPath(absPath),
			CreatedAt:         session.Created,
			UpdatedAt:         session.Updated,
		}
		if idx.CreatedAt == "" {
			idx.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if idx.UpdatedAt == "" {
			idx.UpdatedAt = idx.CreatedAt
		}

		if err := s.db.UpsertChatSessionIndex(idx); err != nil {
			log.Printf("⚠️  index %s: %v", session.ID, err)
			continue
		}
		*count++
	}
	return nil
}
