// Package doctor provides comprehensive health checks and auto-repair for Tofi installations.
//
// Each check module returns []CheckResult with optional fixFunc closures.
// The CLI layer renders results; Fix() executes all fixable items.
package doctor

import (
	"tofi-core/internal/paths"
)

// Severity indicates the importance of a check result.
type Severity int

const (
	SeverityOK   Severity = iota
	SeverityWarn          // non-critical, but should be addressed
	SeverityFail          // critical, blocks operation
	SeverityInfo          // informational only
)

// CheckResult represents a single diagnostic finding.
type CheckResult struct {
	Category  string   // grouping: "Directories", "Environment", etc.
	Label     string   // what was checked
	Severity  Severity // outcome
	Detail    string   // human-readable description
	Fixable   bool     // can be auto-fixed
	FixAction string   // what --fix will do (shown to user)
	fixFunc   func() error
}

// FixResult reports the outcome of an auto-fix attempt.
type FixResult struct {
	Label string
	Fixed bool
	Error string
}

// Report aggregates all check results.
type Report struct {
	Results []CheckResult
	HasFail bool
	HasWarn bool
}

// Options controls which checks to run.
type Options struct {
	HomeDir      string // TOFI_HOME override; empty = use paths.TofiHome()
	CriticalOnly bool   // tofi start preflight: only run blocking checks
}

// Run executes all applicable checks and returns a report.
func Run(opts Options) *Report {
	homeDir := opts.HomeDir
	if homeDir == "" {
		homeDir = paths.TofiHome()
	}

	var results []CheckResult

	// Always run these
	results = append(results, CheckDirectories(homeDir)...)
	results = append(results, CheckConfig(homeDir)...)
	results = append(results, CheckEnvironment()...)

	if !opts.CriticalOnly {
		results = append(results, CheckPythonDeps(homeDir)...)
		results = append(results, CheckSystemSkills(homeDir)...)
		results = append(results, CheckDatabase(homeDir)...)
	}

	report := &Report{Results: results}
	for _, r := range results {
		switch r.Severity {
		case SeverityFail:
			report.HasFail = true
		case SeverityWarn:
			report.HasWarn = true
		}
	}
	return report
}

// Fix executes all fixable items in the report.
func Fix(report *Report) []FixResult {
	var results []FixResult
	for _, r := range report.Results {
		if !r.Fixable || r.fixFunc == nil {
			continue
		}
		fr := FixResult{Label: r.Label}
		if err := r.fixFunc(); err != nil {
			fr.Error = err.Error()
		} else {
			fr.Fixed = true
		}
		results = append(results, fr)
	}
	return results
}

// newOK creates a passing check result.
func newOK(category, label, detail string) CheckResult {
	return CheckResult{Category: category, Label: label, Severity: SeverityOK, Detail: detail}
}

// newWarn creates a warning check result.
func newWarn(category, label, detail string) CheckResult {
	return CheckResult{Category: category, Label: label, Severity: SeverityWarn, Detail: detail}
}

// newFail creates a critical failure check result.
func newFail(category, label, detail string) CheckResult {
	return CheckResult{Category: category, Label: label, Severity: SeverityFail, Detail: detail}
}

// newInfo creates an informational check result.
func newInfo(category, label, detail string) CheckResult {
	return CheckResult{Category: category, Label: label, Severity: SeverityInfo, Detail: detail}
}

// newFixable creates a fixable check result with a repair closure.
func newFixable(category, label, detail, fixAction string, severity Severity, fix func() error) CheckResult {
	return CheckResult{
		Category:  category,
		Label:     label,
		Severity:  severity,
		Detail:    detail,
		Fixable:   true,
		FixAction: fixAction,
		fixFunc:   fix,
	}
}
