package apps

// AppConfig represents the tofi_app.yaml configuration file.
// This is the platform-level config — model, schedule, skills, capabilities.
// Parsed via yaml.Unmarshal directly, no frontmatter splitting needed.
type AppConfig struct {
	// === Meta ===
	ID          string `yaml:"id" json:"id"`     // kebab-case identifier (lowercase + hyphens), used as directory name and DB primary key
	Name        string `yaml:"name" json:"name"` // display name (free-form, any language)
	Description string `yaml:"description" json:"description"`
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	Author      string `yaml:"author,omitempty" json:"author,omitempty"`

	// === Runtime ===
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// === Dependencies ===
	Skills          []string       `yaml:"skills,omitempty" json:"skills,omitempty"`
	RequiredSecrets []string       `yaml:"required_secrets,omitempty" json:"required_secrets,omitempty"`
	Capabilities    map[string]any `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`

	// === User Parameters ===
	Parameters map[string]*AppParameter `yaml:"parameters,omitempty" json:"parameters,omitempty"`

	// === Schedule ===
	Schedule         *AppConfigSchedule `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	BufferSize       int                `yaml:"buffer_size,omitempty" json:"buffer_size,omitempty"`
	RenewalThreshold int                `yaml:"renewal_threshold,omitempty" json:"renewal_threshold,omitempty"`
}

// AppConfigSchedule is the schedule section of tofi_app.yaml.
// Supports the v2 entries format directly.
type AppConfigSchedule struct {
	Entries  []ScheduleEntry `yaml:"entries,omitempty" json:"entries,omitempty"`
	Timezone string          `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

// ScheduleEntry is a single schedule entry.
type ScheduleEntry struct {
	Time    string         `yaml:"time" json:"time"`
	Repeat  ScheduleRepeat `yaml:"repeat" json:"repeat"`
	Enabled bool           `yaml:"enabled" json:"enabled"`
}

// ScheduleRepeat defines how a schedule entry repeats.
type ScheduleRepeat struct {
	Type       string `yaml:"type" json:"type"`                                   // daily, weekdays, weekly, monthly, custom
	DayOfWeek  int    `yaml:"day_of_week,omitempty" json:"day_of_week,omitempty"` // for weekly
	DayOfMonth int    `yaml:"day_of_month,omitempty" json:"day_of_month,omitempty"` // for monthly
	Cron       string `yaml:"cron,omitempty" json:"cron,omitempty"`               // for custom
}

// AgentDef represents the full agent definition (split files + tofi_app.yaml).
//
// File layout:
//   tofi_app.yaml  — platform config (required)
//   AGENTS.md      — operational instructions (required)
//   SOUL.md        — personality, principles, boundaries (optional)
//   IDENTITY.md    — name, emoji, vibe (optional)
type AgentDef struct {
	Config     AppConfig `json:"config"`               // from tofi_app.yaml
	AgentsMD   string    `json:"agents_md"`             // AGENTS.md — operational instructions / task prompt
	SoulMD     string    `json:"soul_md,omitempty"`     // SOUL.md — personality, principles, boundaries
	IdentityMD string    `json:"identity_md,omitempty"` // IDENTITY.md — name, emoji, vibe
	Dir        string    `json:"dir,omitempty"`
	HasScripts bool      `json:"has_scripts,omitempty"`
}

// SystemPrompt builds the combined system prompt from SOUL.md + IDENTITY.md content.
// AGENTS.md is used as the task prompt, not part of the system prompt.
func (d *AgentDef) SystemPrompt() string {
	var parts []string
	if d.IdentityMD != "" {
		parts = append(parts, d.IdentityMD)
	}
	if d.SoulMD != "" {
		parts = append(parts, d.SoulMD)
	}
	if len(parts) == 0 {
		return ""
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n\n---\n\n"
		}
		result += p
	}
	return result
}
