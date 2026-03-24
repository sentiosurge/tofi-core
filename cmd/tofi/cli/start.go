package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"tofi-core/internal/daemon"
	"tofi-core/internal/pkg/logger"
	"tofi-core/internal/server"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	startPort       int
	startForeground bool
	startWorkers    int
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Tofi engine",
	Long:  "Launch the Tofi engine as a background daemon. Use --foreground to run in the current terminal.",
	RunE:  runStart,
}

func init() {
	startCmd.Flags().IntVarP(&startPort, "port", "p", daemon.GetDefaultPort(), "HTTP API port")
	startCmd.Flags().BoolVar(&startForeground, "foreground", false, "run in foreground (don't daemonize)")
	startCmd.Flags().IntVarP(&startWorkers, "workers", "w", 10, "max concurrent workers")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	logger.Init(homeDir)

	if startForeground {
		return runForeground()
	}
	return runDaemon()
}

func runDaemon() error {
	// Ensure workspace exists
	if _, err := os.Stat(homeDir); os.IsNotExist(err) {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ Workspace not found at ") + accentStyle.Render(homeDir))
		fmt.Println(subtitleStyle.Render("  Run ") + accentStyle.Render("tofi init") + subtitleStyle.Render(" first"))
		fmt.Println()
		return fmt.Errorf("workspace not initialized")
	}

	fmt.Println()
	fmt.Println(logo)
	fmt.Println()

	// Start checks
	steps := []struct {
		label string
		check func() error
	}{
		{"Loading workspace", func() error {
			_, err := os.Stat(homeDir)
			return err
		}},
	}

	for _, step := range steps {
		if err := step.check(); err != nil {
			fmt.Printf("  %s %s\n", errorStyle.Render("✗"), step.label)
			return err
		}
		fmt.Printf("  %s %s    %s\n", successStyle.Render("✓"), step.label, subtitleStyle.Render(homeDir))
	}

	fmt.Printf("  %s Starting engine...\n", accentStyle.Render("●"))

	pid, err := daemon.Start(homeDir, startPort, false)
	if err != nil {
		fmt.Printf("  %s %s\n", errorStyle.Render("✗"), err.Error())
		return err
	}

	fmt.Printf("  %s Engine running       %s\n", successStyle.Render("✓"), subtitleStyle.Render(fmt.Sprintf("pid %d", pid)))
	fmt.Printf("  %s Listening on         %s\n", successStyle.Render("✓"), accentStyle.Render(fmt.Sprintf("http://localhost:%d", startPort)))
	fmt.Println()

	badge := lipgloss.NewStyle().
		Background(lipgloss.Color("#238636")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		Render(" Engine ready ")

	fmt.Printf("  %s  %s\n\n", badge, subtitleStyle.Render("Use tofi stop to shut down"))

	return nil
}

// loadJWTSecret reads jwt_secret from config.yaml and sets it as env var
// so the server's auth module uses the same secret as the CLI.
func loadJWTSecret() {
	data, err := os.ReadFile(filepath.Join(homeDir, "config.yaml"))
	if err != nil {
		return
	}
	var cfg struct {
		JWTSecret string `yaml:"jwt_secret"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil || cfg.JWTSecret == "" {
		return
	}
	os.Setenv("TOFI_JWT_SECRET", cfg.JWTSecret)
}

func runForeground() error {
	// Load JWT secret from config so daemon and CLI share the same secret
	loadJWTSecret()

	// Write PID
	daemon.WritePID(homeDir, os.Getpid())
	defer daemon.RemovePID(homeDir)

	mode := os.Getenv("TOFI_MODE")
	if mode == "" {
		mode = "self-hosted"
	}

	cfg := server.Config{
		Port:                   startPort,
		HomeDir:                homeDir,
		MaxConcurrentWorkflows: startWorkers,
		Mode:                   mode,
	}

	srv, err := server.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize server: %w", err)
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Printf("Received shutdown signal, stopping...")
		// Server will handle graceful shutdown
		os.Exit(0)
	}()

	return srv.Start()
}
