package models

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// NodeStat 记录单个节点的运行履历
type NodeStat struct {
	NodeID    string
	Type      string
	Status    string // SUCCESS, ERROR, SKIP
	Duration  time.Duration
	StartTime time.Time
}

// Parameter 定义了节点输入的原子单位
type Parameter struct {
	Var    *VarDefinition    `json:"var,omitempty" yaml:"var,omitempty"`
	Secret *SecretDefinition `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// VarDefinition 是变量类型的参数
type VarDefinition struct {
	ID       string      `json:"id" yaml:"id"`
	Type     string      `json:"type" yaml:"type"` // text, list, dict, bool, number
	Value    interface{} `json:"value" yaml:"value"`
	Optional bool        `json:"optional" yaml:"optional"`
}

// SecretDefinition 是密钥类型的参数
type SecretDefinition struct {
	ID       string `json:"id" yaml:"id"`
	Value    string `json:"value" yaml:"value"` // 引用全局 Secret Key
	Optional bool   `json:"optional" yaml:"optional"`
}

type Node struct {
	ID           string                 `json:"id" yaml:"id"`
	Name         string                 `json:"name" yaml:"name"` // 新增：人类可读名称
	Type         string                 `json:"type" yaml:"type"`
	Value        interface{}            `json:"value,omitempty" yaml:"value,omitempty"`
	Config       map[string]interface{} `json:"config" yaml:"config"` // 修改为 interface{} 以支持数字/布尔字面量
	Input        []Parameter            `json:"input" yaml:"input"`   // 修改为 Parameter 列表
	Env          map[string]string      `json:"env" yaml:"env"`
	RunIf        string                 `json:"run_if" yaml:"run_if"`
	Next         []string               `json:"next" yaml:"next"`
	Dependencies []string               `json:"dependencies" yaml:"dependencies"`
	RetryCount   int                    `json:"retry_count" yaml:"retry_count"`
	OnFailure    []string               `json:"on_failure" yaml:"on_failure"`
	Timeout      int                    `json:"timeout" yaml:"timeout"`
}

// GetRuntimeID 返回节点的最终 ID（如果手动指定了 ID 则优先，否则根据 Name 生成）
func (n *Node) GetRuntimeID() string {
	if n.ID != "" {
		return n.ID
	}
	return NormalizeID(n.Name)
}

// NormalizeID 将名称转换为标准 ID 格式：gpt.write.essay
func NormalizeID(name string) string {
	if name == "" {
		return "unnamed_node"
	}
	// 简单实现：小写 + 空格转点 + 移除特殊字符
	// 实际生产中可以加入拼音转换逻辑
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", ".")
	s = strings.ReplaceAll(s, "-", ".")
	s = strings.ReplaceAll(s, "_", ".")

	// 只保留字母、数字和点
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			sb.WriteRune(r)
		}
	}

	result := sb.String()
	// 处理重复的点
	for strings.Contains(result, "..") {
		result = strings.ReplaceAll(result, "..", ".")
	}
	return strings.Trim(result, ".")
}

type Workflow struct {
	Name  string           `json:"name" yaml:"name"`
	Nodes map[string]*Node `json:"nodes" yaml:"nodes"`
}

type ExecutionPaths struct {
	Home    string
	Logs    string
	States  string
	Reports string
}

type ExecutionContext struct {
	ExecutionID  string
	Paths        ExecutionPaths
	Results      map[string]string
	startedNodes map[string]bool
	Stats        []NodeStat
	mu           sync.RWMutex
	Wg           sync.WaitGroup
	SecretValues []string
}

func NewExecutionContext(execID, homeDir string) *ExecutionContext {
	return &ExecutionContext{
		ExecutionID: execID,
		Paths: ExecutionPaths{
			Home:    homeDir,
			Logs:    filepath.Join(homeDir, "logs"),
			States:  filepath.Join(homeDir, "states"),
			Reports: filepath.Join(homeDir, "reports"),
		},
		Results:      make(map[string]string),
		startedNodes: make(map[string]bool),
		Stats:        []NodeStat{},
	}
}

func (ctx *ExecutionContext) CheckAndSetStarted(nodeID string) bool {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.startedNodes[nodeID] {
		return true
	}
	ctx.startedNodes[nodeID] = true
	return false
}

func (ctx *ExecutionContext) RecordStat(stat NodeStat) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.Stats = append(ctx.Stats, stat)
}

func (ctx *ExecutionContext) SetResult(nodeID, result string) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.Results[nodeID] = result
}

func (ctx *ExecutionContext) GetResult(nodeID string) (string, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	result, ok := ctx.Results[nodeID]
	return result, ok
}

func (ctx *ExecutionContext) ReplaceParams(script string) string {
	result, _ := ctx.replaceParamsInternal(script, false)
	return result
}

func (ctx *ExecutionContext) ReplaceParamsStrict(script string) (string, error) {
	return ctx.replaceParamsInternal(script, true)
}

func (ctx *ExecutionContext) replaceParamsInternal(script string, strict bool) (string, error) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	finalScript := script
	for nodeID, output := range ctx.Results {
		placeholder := "{{" + nodeID + "}}"
		if strings.Contains(finalScript, placeholder) {
			finalScript = strings.ReplaceAll(finalScript, placeholder, output)
		}
		prefix := "{{" + nodeID + "."
		for strings.Contains(finalScript, prefix) {
			startIdx := strings.Index(finalScript, prefix)
			endIdx := strings.Index(finalScript[startIdx:], "}}") + startIdx
			fullPath := finalScript[startIdx+2 : endIdx]
			jsonPath := strings.TrimPrefix(fullPath, nodeID+".")

			result := gjson.Get(output, jsonPath)

			if strict && !result.Exists() {
				return "", fmt.Errorf("字段不存在: {{%s}}\n"+
					"  节点 '%s' 的输出中没有字段 '%s'\n"+
					"  实际输出: %s",
					fullPath, nodeID, jsonPath, truncateString(output, 200))
			}

			value := result.String()
			finalScript = strings.ReplaceAll(finalScript, "{{"+fullPath+"}}", value)
		}
	}

	if strict && strings.Contains(finalScript, "{{") {
		startIdx := strings.Index(finalScript, "{{")
		endIdx := strings.Index(finalScript[startIdx:], "}}") + startIdx
		if endIdx > startIdx {
			unresolvedVar := finalScript[startIdx+2 : endIdx]
			nodeID := unresolvedVar
			if dotIdx := strings.Index(unresolvedVar, "."); dotIdx > 0 {
				nodeID = unresolvedVar[:dotIdx]
			}
			return "", fmt.Errorf("节点不存在: {{%s}}\n"+
				"  引用的节点 '%s' 不存在或尚未执行\n"+
				"  提示: 请检查节点ID拼写和依赖关系",
				unresolvedVar, nodeID)
		}
	}

	return finalScript, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (ctx *ExecutionContext) ReplaceParamsAny(val interface{}) interface{} {
	switch v := val.(type) {
	case string:
		return ctx.ReplaceParams(v)
	case map[string]interface{}:
		newMap := make(map[string]interface{})
		for k, subVal := range v {
			newMap[k] = ctx.ReplaceParamsAny(subVal)
		}
		return newMap
	case []interface{}:
		newSlice := make([]interface{}, len(v))
		for i, subVal := range v {
			newSlice[i] = ctx.ReplaceParamsAny(subVal)
		}
		return newSlice
	default:
		return v
	}
}

type ExecutionResult struct {
	ExecutionID  string            `json:"execution_id"`
	WorkflowName string            `json:"workflow_name"`
	Status       string            `json:"status"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Duration     string            `json:"duration"`
	Stats        []NodeStat        `json:"stats"`
	Outputs      map[string]string `json:"outputs"`
}

func (ctx *ExecutionContext) AddSecretValue(val string) {
	if val != "" {
		ctx.SecretValues = append(ctx.SecretValues, val)
	}
}

func (ctx *ExecutionContext) MaskLog(input string) string {
	output := input
	for _, secret := range ctx.SecretValues {
		output = strings.ReplaceAll(output, secret, "********")
	}
	return output
}

func (ctx *ExecutionContext) Snapshot() (map[string]string, []NodeStat) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	results := make(map[string]string, len(ctx.Results))
	for k, v := range ctx.Results {
		results[k] = v
	}

	stats := make([]NodeStat, len(ctx.Stats))
	copy(stats, ctx.Stats)

	return results, stats
}

func (ctx *ExecutionContext) Clone() *ExecutionContext {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	cloned := &ExecutionContext{
		ExecutionID:  ctx.ExecutionID,
		Paths:        ctx.Paths,
		Results:      make(map[string]string),
		startedNodes: make(map[string]bool),
		Stats:        []NodeStat{},
		SecretValues: make([]string, len(ctx.SecretValues)),
	}

	for k, v := range ctx.Results {
		cloned.Results[k] = v
	}

	copy(cloned.SecretValues, ctx.SecretValues)

	return cloned
}
