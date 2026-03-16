package server

import (
	"tofi-core/internal/workspace"
)

// initWorkspace initializes the file-based workspace and sync manager.
// Call this during server startup, after DB is initialized.
func (s *Server) initWorkspace() {
	ws := workspace.New(s.config.HomeDir)
	wsSync := workspace.NewSync(ws, s.db)

	s.workspace = ws
	s.workspaceSync = wsSync
}

// syncWorkspaceOnStartup scans all user agent directories and syncs to DB index.
func (s *Server) syncWorkspaceOnStartup() {
	if s.workspaceSync != nil {
		s.workspaceSync.SyncOnStartup()
	}
}
