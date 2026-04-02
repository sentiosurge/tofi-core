package agent

import (
	"context"
	"testing"

	"tofi-core/internal/provider"
)

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	reg := NewToolRegistry()

	tool := &FuncTool{
		ToolName: "test_tool",
		ToolSchema: provider.Tool{
			Name:        "test_tool",
			Description: "A test tool",
		},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "ok", nil
		},
		IsConcurrent:   true,
		IsReadOnlyTool: true,
	}

	reg.Register(tool)

	if !reg.Has("test_tool") {
		t.Error("should have test_tool")
	}
	if reg.Has("nonexistent") {
		t.Error("should not have nonexistent")
	}

	got := reg.Get("test_tool")
	if got == nil {
		t.Fatal("Get should return the tool")
	}
	if got.Name() != "test_tool" {
		t.Error("wrong name")
	}
	if !got.ConcurrencySafe() {
		t.Error("should be concurrency safe")
	}
	if !got.ReadOnly() {
		t.Error("should be read only")
	}
}

func TestToolRegistry_Schemas(t *testing.T) {
	reg := NewToolRegistry()

	reg.Register(&FuncTool{
		ToolName:   "tool_a",
		ToolSchema: provider.Tool{Name: "tool_a", Description: "A"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "", nil
		},
	})
	reg.Register(&FuncTool{
		ToolName:   "tool_b",
		ToolSchema: provider.Tool{Name: "tool_b", Description: "B"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "", nil
		},
	})

	schemas := reg.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}
	// Order should be preserved
	if schemas[0].Name != "tool_a" || schemas[1].Name != "tool_b" {
		t.Error("schema order not preserved")
	}
}

func TestToolRegistry_AllConcurrencySafe(t *testing.T) {
	reg := NewToolRegistry()

	reg.Register(&FuncTool{
		ToolName: "safe1", IsConcurrent: true,
		ToolSchema:  provider.Tool{Name: "safe1"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) { return "", nil },
	})
	reg.Register(&FuncTool{
		ToolName: "safe2", IsConcurrent: true,
		ToolSchema:  provider.Tool{Name: "safe2"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) { return "", nil },
	})
	reg.Register(&FuncTool{
		ToolName: "unsafe", IsConcurrent: false,
		ToolSchema:  provider.Tool{Name: "unsafe"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) { return "", nil },
	})

	if !reg.AllConcurrencySafe([]string{"safe1", "safe2"}) {
		t.Error("safe1 + safe2 should be all concurrent safe")
	}
	if reg.AllConcurrencySafe([]string{"safe1", "unsafe"}) {
		t.Error("safe1 + unsafe should not be all concurrent safe")
	}
	if reg.AllConcurrencySafe([]string{"safe1"}) {
		t.Error("single tool should return false (need >1 for parallel)")
	}
}

func TestToolRegistry_DuplicatePanics(t *testing.T) {
	reg := NewToolRegistry()
	tool := &FuncTool{
		ToolName:    "dupe",
		ToolSchema:  provider.Tool{Name: "dupe"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) { return "", nil },
	}

	reg.Register(tool)

	defer func() {
		if r := recover(); r == nil {
			t.Error("duplicate register should panic")
		}
	}()
	reg.Register(tool) // should panic
}

func TestFuncTool_Execute(t *testing.T) {
	tool := &FuncTool{
		ToolName:   "echo",
		ToolSchema: provider.Tool{Name: "echo", Description: "Echo input"},
		ExecuteFunc: func(ctx context.Context, args map[string]interface{}) (string, error) {
			msg, _ := args["message"].(string)
			return "echo: " + msg, nil
		},
		IsConcurrent:   true,
		IsReadOnlyTool: true,
		MaxResultChars: 1000,
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"message": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "echo: hello" {
		t.Errorf("expected 'echo: hello', got %q", result)
	}
	if tool.MaxResultSize() != 1000 {
		t.Error("wrong max result size")
	}
}
