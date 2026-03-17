package server

import (
	"os"
	"path/filepath"

	"tofi-core/internal/workspace"

	"gopkg.in/yaml.v3"
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

// loadAccessToken reads access_token from config.yaml for token-mode auth.
func (s *Server) loadAccessToken() {
	data, err := os.ReadFile(filepath.Join(s.config.HomeDir, "config.yaml"))
	if err != nil {
		return
	}
	var cfg struct {
		AuthMode    string `yaml:"auth_mode"`
		AccessToken string `yaml:"access_token"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}
	if cfg.AuthMode == "token" && cfg.AccessToken != "" {
		s.accessToken = cfg.AccessToken
	}
}
