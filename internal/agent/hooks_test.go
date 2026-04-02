package agent

import (
	"fmt"
	"testing"
)

func TestHooks_NilSafe(t *testing.T) {
	var h *Hooks

	// All calls on nil hooks should be no-ops
	args, err := h.callPreToolCall("test", map[string]interface{}{"key": "val"})
	if err != nil {
		t.Errorf("nil hooks PreToolCall should not error: %v", err)
	}
	if args["key"] != "val" {
		t.Error("nil hooks should passthrough args")
	}

	out, err := h.callPostToolCall("test", nil, "output")
	if err != nil || out != "output" {
		t.Error("nil hooks PostToolCall should passthrough")
	}

	if err := h.callPreAPICall(1, 5, 1000); err != nil {
		t.Error("nil hooks PreAPICall should not error")
	}

	// PostAPICall is void, just verify no panic
	h.callPostAPICall(1, 100, 50, true)
}

func TestHooks_DefaultSafe(t *testing.T) {
	h := DefaultHooks()

	args, err := h.callPreToolCall("test", map[string]interface{}{"a": 1})
	if err != nil || args["a"] != 1 {
		t.Error("default hooks should passthrough")
	}
}

func TestHooks_PreToolCall_ModifyArgs(t *testing.T) {
	h := &Hooks{
		PreToolCall: func(toolName string, input map[string]interface{}) (map[string]interface{}, error) {
			// Inject a default timeout if not set
			if _, ok := input["timeout"]; !ok {
				modified := make(map[string]interface{})
				for k, v := range input {
					modified[k] = v
				}
				modified["timeout"] = 60
				return modified, nil
			}
			return input, nil
		},
	}

	args, err := h.callPreToolCall("tofi_shell", map[string]interface{}{"command": "ls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args["timeout"] != 60 {
		t.Error("hook should have injected timeout=60")
	}
	if args["command"] != "ls" {
		t.Error("hook should preserve original args")
	}
}

func TestHooks_PreToolCall_Block(t *testing.T) {
	h := &Hooks{
		PreToolCall: func(toolName string, input map[string]interface{}) (map[string]interface{}, error) {
			if toolName == "dangerous_tool" {
				return nil, fmt.Errorf("blocked: %s is not allowed", toolName)
			}
			return input, nil
		},
	}

	// Allowed tool
	_, err := h.callPreToolCall("tofi_shell", map[string]interface{}{})
	if err != nil {
		t.Error("tofi_shell should be allowed")
	}

	// Blocked tool
	_, err = h.callPreToolCall("dangerous_tool", map[string]interface{}{})
	if err == nil {
		t.Error("dangerous_tool should be blocked")
	}
}

func TestHooks_PostToolCall_ModifyOutput(t *testing.T) {
	h := &Hooks{
		PostToolCall: func(toolName string, input map[string]interface{}, output string) (string, error) {
			// Redact sensitive data
			if toolName == "tofi_shell" && len(output) > 100 {
				return output[:100] + "\n[output capped by hook]", nil
			}
			return output, nil
		},
	}

	longOutput := ""
	for i := 0; i < 50; i++ {
		longOutput += "line of output\n"
	}

	result, err := h.callPostToolCall("tofi_shell", nil, longOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) >= len(longOutput) {
		t.Error("hook should have capped output")
	}
}

func TestHooks_PreAPICall_Block(t *testing.T) {
	h := &Hooks{
		PreAPICall: func(step, messageCount, estimatedTokens int) error {
			if estimatedTokens > 100000 {
				return fmt.Errorf("token budget exceeded: %d > 100000", estimatedTokens)
			}
			return nil
		},
	}

	if err := h.callPreAPICall(1, 10, 50000); err != nil {
		t.Error("50000 tokens should be allowed")
	}
	if err := h.callPreAPICall(2, 20, 150000); err == nil {
		t.Error("150000 tokens should be blocked")
	}
}

func TestHooks_PostAPICall_Tracking(t *testing.T) {
	var totalCalls int
	var totalInput int64

	h := &Hooks{
		PostAPICall: func(step int, inputTokens, outputTokens int64, hasToolCalls bool) {
			totalCalls++
			totalInput += inputTokens
		},
	}

	h.callPostAPICall(1, 1000, 500, true)
	h.callPostAPICall(2, 2000, 800, false)

	if totalCalls != 2 {
		t.Errorf("expected 2 calls tracked, got %d", totalCalls)
	}
	if totalInput != 3000 {
		t.Errorf("expected 3000 total input, got %d", totalInput)
	}
}
