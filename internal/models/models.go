package models

import (
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

type Node struct {
	ID           string            `json:"id" yaml:"id"`
	Type         string            `json:"type" yaml:"type"`
	Config       map[string]string `json:"config" yaml:"config"`
	Input        map[string]interface{} `json:"input" yaml:"input"` // 支持任意结构
	Env          map[string]string      `json:"env" yaml:"env"`
	Data         map[string]interface{} `json:"data" yaml:"data"`
	Next         []string          `json:"next" yaml:"next"`
	Dependencies []string          `json:"dependencies" yaml:"dependencies"`
	RetryCount   int               `json:"retry_count" yaml:"retry_count"`
	OnFailure    []string          `json:"on_failure" yaml:"on_failure"`
	Timeout      int               `json:"timeout" yaml:"timeout"`
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
	Paths        ExecutionPaths // 新增：路径配置
	Results      map[string]string
	startedNodes map[string]bool // 内部使用：防止重复启动
	Stats        []NodeStat      // 记录所有节点的执行统计
	mu           sync.RWMutex
	Wg           sync.WaitGroup
	SecretValues []string
}

// NewExecutionContext 是你需要的构造函数
// 它负责把 Results, startedNodes 这些 Map 初始化好
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

// CheckAndSetStarted 检查并标记节点为已启动
func (ctx *ExecutionContext) CheckAndSetStarted(nodeID string) bool {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.startedNodes[nodeID] {
		return true
	}
	ctx.startedNodes[nodeID] = true
	return false
}

// RecordStat 安全地记录统计数据
func (ctx *ExecutionContext) RecordStat(stat NodeStat) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.Stats = append(ctx.Stats, stat)
}

// SetResult 存入结果
func (ctx *ExecutionContext) SetResult(nodeID, result string) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.Results[nodeID] = result
}

// GetResult 读取结果
func (ctx *ExecutionContext) GetResult(nodeID string) (string, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	result, ok := ctx.Results[nodeID]
	return result, ok
}

// ReplaceParams 变量替换逻辑 (支持 JSON 路径)
func (ctx *ExecutionContext) ReplaceParams(script string) string {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	finalScript := script
	for nodeID, output := range ctx.Results {
		// 基础替换
		placeholder := "{{" + nodeID + "}}"
		if strings.Contains(finalScript, placeholder) {
			finalScript = strings.ReplaceAll(finalScript, placeholder, output)
		}
		// JSON 提取
		prefix := "{{" + nodeID + "."
		for strings.Contains(finalScript, prefix) {
			startIdx := strings.Index(finalScript, prefix)
			endIdx := strings.Index(finalScript[startIdx:], "}}") + startIdx
			fullPath := finalScript[startIdx+2 : endIdx]
			jsonPath := strings.TrimPrefix(fullPath, nodeID+".")
			value := gjson.Get(output, jsonPath).String()
			finalScript = strings.ReplaceAll(finalScript, "{{"+fullPath+"}}", value)
		}
	}
	return finalScript
}

// ReplaceParamsAny 递归处理任意类型中的字符串变量替换
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

// ExecutionResult 代表一次完整工作流运行的最终产物
type ExecutionResult struct {
	ExecutionID  string            `json:"execution_id"`
	WorkflowName string            `json:"workflow_name"`
	Status       string            `json:"status"` // SUCCESS, FAILED, PARTIAL
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Duration     string            `json:"duration"`
	Stats        []NodeStat        `json:"stats"`   // 每个节点的详细履历
	Outputs      map[string]string `json:"outputs"` // 最终所有的 Results 映射
}

func (ctx *ExecutionContext) AddSecretValue(val string) {
	if val != "" {
		ctx.SecretValues = append(ctx.SecretValues, val)
	}
}

// MaskLog 对字符串进行全局脱敏
func (ctx *ExecutionContext) MaskLog(input string) string {
	output := input
	for _, secret := range ctx.SecretValues {
		output = strings.ReplaceAll(output, secret, "********")
	}
	return output
}

// Snapshot 安全地导出当前上下文的快照副本
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