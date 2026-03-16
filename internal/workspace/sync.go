package workspace

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// Sync provides bidirectional sync between filesystem agents and DB index.
type Sync struct {
	ws *Workspace
	db *storage.DB
}

// NewSync creates a new sync manager.
func NewSync(ws *Workspace, db *storage.DB) *Sync {
	return &Sync{ws: ws, db: db}
}

// SyncAgentToDB reads a single agent from filesystem and upserts it into the DB.
// If the agent already exists (by name+userID), it updates; otherwise inserts.
func (s *Sync) SyncAgentToDB(userID, agentName string) (*storage.AppRecord, error) {
	app, err := s.ws.ReadAgent(userID, agentName)
	if err != nil {
		return nil, fmt.Errorf("read agent %q: %w", agentName, err)
	}

	record := AgentDefToRecord(userID, app)

	// Check if agent already exists in DB by name+userID
	existing, err := s.db.GetAppByName(userID, agentName)
	if err == nil && existing != nil {
		// Update existing record
		record.ID = existing.ID
		if err := s.db.UpdateApp(record); err != nil {
			return nil, fmt.Errorf("update agent index: %w", err)
		}
		return record, nil
	}

	// Insert new record
	record.ID = uuid.New().String()
	if err := s.db.CreateApp(record); err != nil {
		return nil, fmt.Errorf("create agent index: %w", err)
	}
	return record, nil
}

// SyncAllAgentsToDB scans a user's agents directory and syncs all to DB.
// Removes DB entries for agents that no longer exist on disk.
func (s *Sync) SyncAllAgentsToDB(userID string) error {
	names, err := s.ws.ListAgents(userID)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	// Build set of agents on disk
	diskAgents := make(map[string]bool, len(names))
	for _, name := range names {
		diskAgents[name] = true
		if _, err := s.SyncAgentToDB(userID, name); err != nil {
			log.Printf("[workspace-sync] warning: failed to sync agent %q: %v", name, err)
		}
	}

	// Remove DB entries that no longer exist on disk
	dbApps, err := s.db.ListApps(userID)
	if err != nil {
		return fmt.Errorf("list db apps: %w", err)
	}
	for _, app := range dbApps {
		if app.Source == "local" && !diskAgents[app.Name] {
			log.Printf("[workspace-sync] removing orphan DB entry: %q", app.Name)
			s.db.DeleteApp(app.ID, userID)
		}
	}

	return nil
}

// MigrateDBToFiles exports DB records that have no corresponding files on disk.
// This is a one-time migration: for each DB app record, if no agent directory exists,
// convert the record to an AgentDef and write it to disk.
func (s *Sync) MigrateDBToFiles() {
	allApps, err := s.db.ListAllApps()
	if err != nil {
		log.Printf("[workspace-migrate] failed to list all apps: %v", err)
		return
	}

	migrated := 0
	for _, record := range allApps {
		if record.Name == "" || record.UserID == "" {
			continue
		}

		// Check if agent files already exist (tofi_app.yaml is the marker)
		agentDir := s.ws.AgentDir(record.UserID, record.Name)
		yamlPath := filepath.Join(agentDir, AppYAMLFile)
		if _, err := os.Stat(yamlPath); err == nil {
			// Files already exist — skip (files are source of truth)
			continue
		}

		// Convert DB record to AgentDef and write files
		def := RecordToAgentDef(record)
		if err := s.ws.WriteAgent(record.UserID, def); err != nil {
			log.Printf("[workspace-migrate] failed to write agent %q for user %q: %v",
				record.Name, record.UserID, err)
			continue
		}

		// Mark as local source in DB
		if record.Source == "" {
			record.Source = "local"
			_ = s.db.UpdateApp(record)
		}

		migrated++
		log.Printf("[workspace-migrate] exported %q (user=%s) to files", record.Name, record.UserID)
	}

	if migrated > 0 {
		log.Printf("[workspace-migrate] migrated %d apps from DB to files", migrated)
	}
}

// SyncOnStartup first migrates DB records to files, then scans all user directories and syncs agents to DB.
func (s *Sync) SyncOnStartup() {
	// Phase 1: Migrate existing DB records to files (one-time)
	s.MigrateDBToFiles()

	// Phase 2: Sync files → DB (files are source of truth)
	usersDir := s.ws.homeDir + "/users"
	entries, err := readDirSafe(usersDir)
	if err != nil {
		log.Printf("[workspace-sync] no users directory yet, skipping startup sync")
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		userID := entry.Name()
		if err := s.SyncAllAgentsToDB(userID); err != nil {
			log.Printf("[workspace-sync] failed to sync user %q: %v", userID, err)
		}
	}
	log.Printf("[workspace-sync] startup sync complete")
}
