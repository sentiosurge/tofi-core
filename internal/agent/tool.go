package agent

import (
	"context"

	"tofi-core/internal/provider"
)

// ToolDef is the unified interface for all tools in the agent loop.
// Each tool declares its schema, execution behavior, and safety properties.
// This enables the scheduler to make informed decisions about parallel execution,
// result size limits, and permission checks.
type ToolDef interface {
	// Name returns the tool's internal ID (e.g. "tofi__read"). Must be unique.
	Name() string

	// DisplayName returns the user-facing name for TUI/Web (e.g. "Read File").
	// Internal ID is for LLM tool_call; display name is what users see.
	DisplayName() string

	// Schema returns the provider.Tool definition for LLM registration.
	Schema() provider.Tool

	// Execute runs the tool with parsed arguments.
	// Returns the result string and any error.
	Execute(ctx context.Context, args map[string]interface{}) (string, error)

	// ConcurrencySafe returns true if this tool can run in parallel with others.
	// Read-only tools and independent API calls should return true.
	// Tools that modify shared state (filesystem, sandbox) should return false.
	ConcurrencySafe() bool

	// ReadOnly returns true if the tool does not modify any state.
	// Used for permission decisions and audit logging.
	ReadOnly() bool

	// MaxResultSize returns the maximum number of characters to keep from the result.
	// 0 means no limit (use default truncation from smartTruncate).
	MaxResultSize() int
}

// ToolRegistry manages a collection of tools with lookup and schema generation.
type ToolRegistry struct {
	tools    map[string]ToolDef
	order    []string // preserves registration order
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]ToolDef),
	}
}

// Register adds a tool to the registry. Panics on duplicate name.
func (r *ToolRegistry) Register(tool ToolDef) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		panic("duplicate tool registration: " + name)
	}
	r.tools[name] = tool
	r.order = append(r.order, name)
}

// Get returns a tool by name, or nil if not found.
func (r *ToolRegistry) Get(name string) ToolDef {
	return r.tools[name]
}

// Has returns true if a tool with the given name is registered.
func (r *ToolRegistry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Schemas returns all tool schemas for LLM registration.
func (r *ToolRegistry) Schemas() []provider.Tool {
	schemas := make([]provider.Tool, 0, len(r.order))
	for _, name := range r.order {
		schemas = append(schemas, r.tools[name].Schema())
	}
	return schemas
}

// Names returns all registered tool names in order.
func (r *ToolRegistry) Names() []string {
	result := make([]string, len(r.order))
	copy(result, r.order)
	return result
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	return len(r.tools)
}

// DisplayNameFor returns the display name for a tool, or the internal name if not found.
func (r *ToolRegistry) DisplayNameFor(name string) string {
	if tool, ok := r.tools[name]; ok {
		return tool.DisplayName()
	}
	return name
}

// AllConcurrencySafe returns true if all given tool names are concurrency-safe.
func (r *ToolRegistry) AllConcurrencySafe(names []string) bool {
	for _, name := range names {
		tool := r.tools[name]
		if tool == nil || !tool.ConcurrencySafe() {
			return false
		}
	}
	return len(names) > 1
}

// FuncTool is a simple ToolDef implementation backed by a function.
// Use for quick tool creation without defining a full struct.
type FuncTool struct {
	ToolName        string
	ToolDisplayName string
	ToolSchema      provider.Tool
	ExecuteFunc     func(ctx context.Context, args map[string]interface{}) (string, error)
	IsConcurrent    bool
	IsReadOnlyTool  bool
	MaxResultChars  int
}

func (f *FuncTool) Name() string                      { return f.ToolName }
func (f *FuncTool) DisplayName() string {
	if f.ToolDisplayName != "" {
		return f.ToolDisplayName
	}
	return f.ToolName
}
func (f *FuncTool) Schema() provider.Tool              { return f.ToolSchema }
func (f *FuncTool) ConcurrencySafe() bool              { return f.IsConcurrent }
func (f *FuncTool) ReadOnly() bool                     { return f.IsReadOnlyTool }
func (f *FuncTool) MaxResultSize() int                 { return f.MaxResultChars }

func (f *FuncTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return f.ExecuteFunc(ctx, args)
}
