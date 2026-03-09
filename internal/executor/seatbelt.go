package executor

import (
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// seatbeltAvailable is set at init time — true if macOS sandbox-exec exists.
var seatbeltAvailable bool

func init() {
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := exec.LookPath("sandbox-exec"); err == nil {
		seatbeltAvailable = true
	}
}

// SeatbeltConfig configures the macOS sandbox-exec (Seatbelt) security policy.
type SeatbeltConfig struct {
	DenyReadPaths   []string // Paths to deny reading
	DenyWritePaths  []string // Paths to deny writing
	DenyNetworkBind bool     // Block port binding (prevents reverse shells)
	DenyExecutables []string // Block specific executables
}

// defaultSeatbeltConfig returns the default security policy.
// Uses "allow default" + deny blacklist: Agent needs broad access to system
// resources (python, node, pip, curl), so we deny only sensitive paths.
func defaultSeatbeltConfig() SeatbeltConfig {
	home := getUserHome()
	return SeatbeltConfig{
		DenyReadPaths: []string{
			home + "/.ssh",
			home + "/.aws",
			home + "/.gnupg",
			home + "/.kube",
			home + "/.config/gcloud",
			home + "/Library/Keychains",
			home + "/.docker/config.json",
		},
		DenyWritePaths: []string{
			home + "/.ssh",
			home + "/.aws",
			home + "/.gnupg",
			home + "/.bashrc",
			home + "/.zshrc",
			home + "/.bash_profile",
			home + "/.zprofile",
		},
		DenyNetworkBind: true,
		DenyExecutables: []string{
			"/usr/bin/nc",
			"/usr/bin/ncat",
		},
	}
}

// buildSeatbeltProfile generates an SBPL (Sandbox Profile Language) string
// for macOS sandbox-exec.
func buildSeatbeltProfile(cfg SeatbeltConfig) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n")
	sb.WriteString("(allow default)\n")

	// Deny reading sensitive paths
	for _, path := range cfg.DenyReadPaths {
		sb.WriteString(fmt.Sprintf("(deny file-read* (subpath %q))\n", path))
	}

	// Deny writing sensitive paths
	for _, path := range cfg.DenyWritePaths {
		sb.WriteString(fmt.Sprintf("(deny file-write* (subpath %q))\n", path))
	}

	// Deny binding ports (blocks reverse shell listeners)
	if cfg.DenyNetworkBind {
		sb.WriteString("(deny network-bind)\n")
	}

	// Deny specific executables
	for _, exe := range cfg.DenyExecutables {
		sb.WriteString(fmt.Sprintf("(deny process-exec (literal %q))\n", exe))
	}

	return sb.String()
}

// getUserHome returns the current user's home directory.
func getUserHome() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return "/Users/unknown"
}
