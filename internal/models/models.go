package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/pkg/logger"

	"github.com/tidwall/gjson"
)

// NodeStat 记录单个节点的运行履历
type NodeStat struct {
	NodeID    string        `json:"node_id"`
	Type      string        `json:"type"`
	Status    string        `json:"status"` // SUCCESS, ERROR, SKIP
	Duration  time.Duration `json:"duration"`
	StartTime time.Time     `json:"start_time"`
}

// Parameter 定义了节点输入的原子单位
type Parameter struct {
	Var    *VarDefinition    `json:"var,omitempty" yaml:"var,omitempty"`
	Secret *SecretDefinition `json:"secret,omitempty" yaml:"secret,omitempty"`
	File   *FileDefinition   `json:"file,omitempty" yaml:"file,omitempty"`
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

// FileDefinition 是文件类型的参数 (Schem B: Explicit File Ref)
type FileDefinition struct {
	ID       string `json:"id" yaml:"id"`             // Input 变量名 (e.g. "my_dataset")
	FileRef  string `json:"file_ref" yaml:"file_ref"` // 用户可读 ID (e.g. "sales_data_2024")
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
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", ".")
	s = strings.ReplaceAll(s, "-", ".")
	s = strings.ReplaceAll(s, "_", ".")

	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			sb.WriteRune(r)
		}
	}

	result := sb.String()
	for strings.Contains(result, "..") {
		result = strings.ReplaceAll(result, "..", ".")
	}
	return strings.Trim(result, ".")
}

type Workflow struct {
	ID          string                 `json:"id" yaml:"id"`
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description" yaml:"description"`
	Icon        string                 `json:"icon" yaml:"icon"`
	Data        map[string]interface{} `json:"data" yaml:"data"`
	Secrets     map[string]string      `json:"secrets" yaml:"secrets"`
	Nodes       map[string]*Node       `json:"nodes" yaml:"nodes"`
	Timeout     int                    `json:"timeout" yaml:"timeout"` // 全局工作流超时（秒）
}

type UserFileRecord struct {
	UUID             string `json:"uuid"`
	FileID           string `json:"file_id"`
	User             string `json:"user"`
	OriginalFilename string `json:"original_filename"`
	MimeType         string `json:"mime_type"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
	Hash             string `json:"hash"`
	// New fields for File ID system
	WorkflowID string `json:"workflow_id,omitempty"` // Associated workflow (optional)
	NodeID     string `json:"node_id,omitempty"`     // Associated node (optional)
	Source     string `json:"source,omitempty"`      // "library" | "workflow"
}

type ArtifactRecord struct {
	ID           string `json:"id"`
	ExecutionID  string `json:"execution_id"`
	WorkflowName string `json:"workflow_name"` // Joined from executions
	Filename     string `json:"filename"`
	RelativePath string `json:"relative_path"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	CreatedAt    string `json:"created_at"`
}

type ExecutionPaths struct {
	Home      string
	Logs      string
	States    string
	Reports   string
	Artifacts string // 存放产物 (Output)
}

type ExecutionContext struct {
	ExecutionID  string
	WorkflowID   string // 工作流 ID
	WorkflowName string // 工作流名称
	User         string // 执行用户 (租户)
	Paths        ExecutionPaths
	Results      map[string]string
	Approvals    map[string]string // 存储人工审批状态: nodeID -> action (approve/reject)
	startedNodes map[string]bool
	Stats        []NodeStat
	mu           sync.RWMutex
	Wg           sync.WaitGroup
	SecretValues []string
	Depth        int                // 递归深度 (防止死循环)
	DB           interface{}        // 存储对象 (storage.DB), 使用 interface 避免循环引用
	Ctx          context.Context    // 用于超时控制
	Cancel       context.CancelFunc // 用于取消执行

	// UpstreamContent stores content from upstream nodes for File nodes
	// Key: nodeID, Value: raw content string
	// Used for resolving {{file_node.content}} when file is not saved to disk
	UpstreamContent map[string]string
}

func NewExecutionContext(execID, user, homeDir string) *ExecutionContext {
	if user == "" {
		user = "admin"
	}

	// Initial paths (WorkflowName is empty initially)
	userBase := filepath.Join(homeDir, user)

	ctx, cancel := context.WithCancel(context.Background())

	return &ExecutionContext{
		ExecutionID: execID,
		User:        user,
		Paths: ExecutionPaths{
			Home:    homeDir,
			Logs:    filepath.Join(userBase, "logs"),
			Reports: filepath.Join(userBase, "reports"),
		},
		Results:      make(map[string]string),
		Approvals:    make(map[string]string),
		startedNodes: make(map[string]bool),
		Stats:        []NodeStat{},
		Depth:        0,
		Ctx:          ctx,
		Cancel:       cancel,
	}
}

func (ctx *ExecutionContext) ApproveNode(nodeID, action string) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.Approvals[nodeID] = action
}

func (ctx *ExecutionContext) GetApproval(nodeID string) (string, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	val, ok := ctx.Approvals[nodeID]
	return val, ok
}

// SetWorkflowName sets the name and updates relevant paths like Artifacts
// Artifacts are isolated per execution to prevent overwrites between runs
func (ctx *ExecutionContext) SetWorkflowName(name string) {
	ctx.WorkflowName = name
	ctx.Paths.Artifacts = filepath.Join(
		ctx.Paths.Home, ctx.User, "artifacts",
		NormalizeID(name), ctx.ExecutionID,
	)
}

// Log 封装日志调用
func (ctx *ExecutionContext) Log(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	msg = ctx.MaskLog(msg)

	// Use global system logger (stdout + rotating file)
	logger.Printf("[%s] %s", ctx.ExecutionID, msg)

	// Determine Log Type for DB
	logType := "info"
	cleanMsg := msg

	if strings.Contains(msg, "<think>") {
		logType = "think"
		cleanMsg = strings.ReplaceAll(strings.ReplaceAll(msg, "<think>", ""), "</think>", "")
	} else if strings.Contains(msg, "<tool_call") {
		logType = "tool_call"
	} else if strings.Contains(msg, "[Result]") {
		logType = "tool_result"
	} else if strings.Contains(msg, "[Error]") || strings.Contains(msg, "Failed") {
		logType = "error"
	}

	// Persist to DB if available
	if ctx.DB != nil {
		if db, ok := ctx.DB.(interface {
			AddLog(execID, nodeID, logType, content string) error
		}); ok {
			_ = db.AddLog(ctx.ExecutionID, "", logType, cleanMsg)
		}
	}
}

// Close 释放资源
func (ctx *ExecutionContext) Close() {
	if ctx.Cancel != nil {
		ctx.Cancel()
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

	// 1. System Variables Replacement
	sysVars := map[string]string{
		"{{ctx.execution_id}}":  ctx.ExecutionID,
		"{{ctx.user}}":          ctx.User,
		"{{ctx.workflow_name}}": ctx.WorkflowName,
	}
	for k, v := range sysVars {
		if strings.Contains(finalScript, k) {
			finalScript = strings.ReplaceAll(finalScript, k, v)
		}
	}

	// 2. Node Output Replacement
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

			// Special handling for File node .content field
			// File node output is JSON without content, so we need to resolve it on demand
			if jsonPath == "content" {
				// Check if this is a File node output (has "path" and "mime_type" fields)
				if gjson.Get(output, "path").Exists() && gjson.Get(output, "mime_type").Exists() {
					content, err := ctx.resolveFileContent(nodeID, output)
					if err != nil {
						if strict {
							return "", fmt.Errorf("无法读取文件内容: {{%s}}\n  %v", fullPath, err)
						}
						// Non-strict mode: replace with error message
						finalScript = strings.ReplaceAll(finalScript, "{{"+fullPath+"}}", fmt.Sprintf("[Error: %v]", err))
						continue
					}
					finalScript = strings.ReplaceAll(finalScript, "{{"+fullPath+"}}", content)
					continue
				}
			}

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

// resolveFileContent reads the content of a file referenced by a File node
// This is called when {{file_node.content}} is referenced
func (ctx *ExecutionContext) resolveFileContent(nodeID, output string) (string, error) {
	path := gjson.Get(output, "path").String()
	mimeType := gjson.Get(output, "mime_type").String()

	// Case 1: File has a path (saved to disk or user uploaded)
	if path != "" {
		// Check if it's a text file
		if !isTextMimeType(mimeType) {
			return "", fmt.Errorf("无法读取二进制文件内容 (%s)，请使用 .path 获取文件路径", mimeType)
		}

		// Read file content
		content, err := readFileContent(path)
		if err != nil {
			return "", err
		}
		return content, nil
	}

	// Case 2: Upstream data not saved to disk - check UpstreamContent
	if ctx.UpstreamContent != nil {
		if content, ok := ctx.UpstreamContent[nodeID]; ok {
			return content, nil
		}
	}

	return "", fmt.Errorf("文件内容不可用：文件未保存到磁盘且无上游数据")
}

// isTextMimeType checks if a MIME type is a text format
func isTextMimeType(mimeType string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	textMimes := map[string]bool{
		"application/json":       true,
		"application/javascript": true,
		"application/typescript": true,
		"application/xml":        true,
		"application/yaml":       true,
		"application/x-yaml":     true,
	}
	return textMimes[mimeType]
}

// readFileContent reads and returns file content with size limit
func readFileContent(path string) (string, error) {
	content, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("无法读取文件: %v", err)
	}

	// Limit content size to prevent memory issues (max 1MB)
	if len(content) > 1024*1024 {
		content = content[:1024*1024]
	}

	return string(content), nil
}

// readFile is a simple file reader (avoiding import os in this file)
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
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
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	Status       string                 `json:"status"`
	StartTime    time.Time              `json:"start_time"`
	EndTime      time.Time              `json:"end_time"`
	Duration     string                 `json:"duration"`
	Stats        []NodeStat             `json:"stats"`
	Outputs      map[string]interface{} `json:"outputs"`
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

// MaskedSnapshot 返回脱敏后的快照，用于 API 返回和数据库存储
// 所有包含 secrets 的输出都会被替换为 ********，且尝试解析 JSON 字符串
func (ctx *ExecutionContext) MaskedSnapshot() (map[string]interface{}, []NodeStat) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	results := make(map[string]interface{}, len(ctx.Results))
	for k, v := range ctx.Results {
		// 对每个输出进行脱敏
		masked := ctx.maskString(v)
		// 尝试智能解析 JSON
		var obj interface{}
		// 只有看起来像 JSON 对象或数组的才尝试解析，避免数字/布尔值的误判
		if (strings.HasPrefix(masked, "{") && strings.HasSuffix(masked, "}")) ||
			(strings.HasPrefix(masked, "[") && strings.HasSuffix(masked, "]")) {
			if err := json.Unmarshal([]byte(masked), &obj); err == nil {
				results[k] = obj
			} else {
				results[k] = masked
			}
		} else {
			results[k] = masked
		}
	}

	stats := make([]NodeStat, len(ctx.Stats))
	copy(stats, ctx.Stats)

	return results, stats
}

// maskString 内部方法，对字符串进行脱敏（不加锁，调用方需持有锁）
func (ctx *ExecutionContext) maskString(input string) string {
	output := input
	for _, secret := range ctx.SecretValues {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "********")
		}
	}
	return output
}

// Clone 深度拷贝当前的上下文
func (ctx *ExecutionContext) Clone() *ExecutionContext {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	cloned := &ExecutionContext{
		ExecutionID:  ctx.ExecutionID,
		WorkflowID:   ctx.WorkflowID,
		WorkflowName: ctx.WorkflowName,
		User:         ctx.User,
		Paths:        ctx.Paths,
		Results:      make(map[string]string),
		startedNodes: make(map[string]bool),
		Stats:        []NodeStat{},
		SecretValues: make([]string, len(ctx.SecretValues)),
		Depth:        ctx.Depth,
		DB:           ctx.DB,
		Ctx:          ctx.Ctx, // 继承父 context
		Cancel:       ctx.Cancel,
	}

	for k, v := range ctx.Results {
		cloned.Results[k] = v
	}

	copy(cloned.SecretValues, ctx.SecretValues)

	return cloned
}

// Derive 创建一个派生的子上下文，用于 Loop 等场景
func (ctx *ExecutionContext) Derive(subID string) *ExecutionContext {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	newID := ctx.ExecutionID + "/" + subID

	// 路径偏移：在原有目录下追加子目录
	newPaths := ctx.Paths
	newPaths.Artifacts = filepath.Join(ctx.Paths.Artifacts, subID)

	derived := &ExecutionContext{
		ExecutionID:  newID,
		WorkflowID:   ctx.WorkflowID,
		WorkflowName: ctx.WorkflowName,
		User:         ctx.User,
		Paths:        newPaths,
		Results:      make(map[string]string),
		startedNodes: make(map[string]bool),
		Stats:        []NodeStat{},
		SecretValues: make([]string, len(ctx.SecretValues)),
		Depth:        ctx.Depth, // 深度不变，因为 Loop 同级
		DB:           ctx.DB,
		Ctx:          ctx.Ctx, // 继承父 context
		Cancel:       ctx.Cancel,
	}

	// 继承结果
	for k, v := range ctx.Results {
		derived.Results[k] = v
	}
	copy(derived.SecretValues, ctx.SecretValues)

	return derived
}
