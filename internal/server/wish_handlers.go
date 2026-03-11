package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tofi-core/internal/capability"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// handleWish POST /api/v1/wish — 许愿：创建 Kanban 卡片 + 异步执行 Agent
func (s *Server) handleWish(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Model       string `json:"model"` // 可选覆盖模型
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	// 1. 创建 Kanban 卡片（todo 状态）
	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       req.Title,
		Description: req.Description,
		Status:      "todo",
		UserID:      userID,
	}

	if err := s.db.CreateKanbanCard(card); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 获取完整的卡片
	created, _ := s.db.GetKanbanCard(card.ID)
	if created == nil {
		created = card
	}

	// 2. 异步执行 Agent（自动检测可用 key）
	go s.executeWish(created, userID, req.Model)

	// 3. 立即返回卡片（不等待执行完成）
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// resolveModelAndKey 智能检测可用的 API Key 和模型
// 优先级：用户指定 model > Settings 中有 key 的 provider > 环境变量中有 key 的 provider
func (s *Server) resolveModelAndKey(userID, requestedModel string) (model, apiKey, provider string, err error) {
	// 1. 用户指定了 model → 根据 model 找 key
	if requestedModel != "" {
		provider = detectProvider(requestedModel)
		apiKey = s.findAPIKey(provider, userID)
		if apiKey != "" {
			return requestedModel, apiKey, provider, nil
		}
		return "", "", "", fmt.Errorf("no API key found for model '%s' (provider: %s)", requestedModel, provider)
	}

	// 2. 自动检测：按优先级尝试各 provider
	// 优先 Settings 表中的 key，再回退到环境变量
	providers := []struct {
		name         string
		defaultModel string
		envKey       string
	}{
		{"anthropic", "claude-sonnet-4-20250514", "TOFI_ANTHROPIC_API_KEY"},
		{"openai", "gpt-4o", "TOFI_OPENAI_API_KEY"},
		{"gemini", "gemini-2.0-flash", "TOFI_GEMINI_API_KEY"},
	}

	for _, p := range providers {
		key := s.findAPIKey(p.name, userID)
		if key != "" {
			log.Printf("🔑 Auto-detected provider: %s", p.name)
			return p.defaultModel, key, p.name, nil
		}
	}

	return "", "", "", fmt.Errorf("no API key configured. Go to Settings to add one, or set TOFI_OPENAI_API_KEY / TOFI_ANTHROPIC_API_KEY env var")
}

// findAPIKey 从 Settings 表和环境变量中查找 API Key
func (s *Server) findAPIKey(provider, userID string) string {
	// 1. Settings 表（user > system）
	key, err := s.db.ResolveAIKey(provider, userID)
	if err == nil && key != "" {
		return key
	}
	// claude → anthropic alias
	if provider == "claude" {
		key, err = s.db.ResolveAIKey("anthropic", userID)
		if err == nil && key != "" {
			return key
		}
	}

	// 2. 环境变量
	envMap := map[string]string{
		"openai":    "TOFI_OPENAI_API_KEY",
		"anthropic": "TOFI_ANTHROPIC_API_KEY",
		"claude":    "TOFI_ANTHROPIC_API_KEY",
		"gemini":    "TOFI_GEMINI_API_KEY",
	}
	if envName, ok := envMap[provider]; ok {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}

	return ""
}

// executeWish 异步执行愿望：匹配 Skills → 组装 Agent → 调用 LLM → 更新卡片
func (s *Server) executeWish(card *storage.KanbanCardRecord, userID, requestedModel string) {
	cardID := card.ID
	log.Printf("🌟 [wish:%s] Starting wish execution: %s", cardID[:8], card.Title)

	// Wrap DB with SSE publisher
	updater := &KanbanUpdaterWithSSE{inner: s.db, hub: s.sseHub, db: s.db}
	defer s.sseHub.CleanupCard(cardID)

	// 更新状态为 working
	updater.UpdateKanbanCardBySystem(cardID, "working", 10, "")
	updater.AppendKanbanStep(cardID, map[string]any{"name": "Initializing", "status": "done"})

	// 1. 获取 Skills
	// App 运行时仅加载 App 配置的 skills，不加载全部
	appID := card.AppID
	if appID == "" {
		appID = card.AgentID
	}
	var installedSkills []*storage.SkillRecord
	if appID != "" {
		// App run: load only configured skills
		if appRec, err := s.db.GetApp(appID); err == nil {
			var skillNames []string
			json.Unmarshal([]byte(appRec.Skills), &skillNames)
			for _, name := range skillNames {
				if rec, err := s.db.GetSkillByName(userID, name); err == nil {
					installedSkills = append(installedSkills, rec)
				} else {
					log.Printf("⚠️ [wish:%s] App skill %q not found: %v", cardID[:8], name, err)
				}
			}
			log.Printf("📦 [wish:%s] App run: loaded %d/%d configured skills", cardID[:8], len(installedSkills), len(skillNames))
		}
	} else {
		// Ad-hoc wish: load all installed skills
		var err error
		installedSkills, err = s.db.ListSkills(userID)
		if err != nil {
			log.Printf("❌ [wish:%s] Failed to list skills: %v", cardID[:8], err)
			updater.UpdateKanbanCardBySystem(cardID, "failed", 0, fmt.Sprintf("Failed to list skills: %v", err))
			return
		}
	}

	// 2. 智能检测 API Key 和模型
	model, apiKey, provider, err := s.resolveModelAndKey(userID, requestedModel)
	if err != nil {
		log.Printf("❌ [wish:%s] %v", cardID[:8], err)
		updater.UpdateKanbanCardBySystem(cardID, "failed", 0, err.Error())
		return
	}
	log.Printf("🔑 [wish:%s] Using model=%s, provider=%s", cardID[:8], model, provider)

	updater.UpdateKanbanCardBySystem(cardID, "working", 20, "")
	updater.AppendKanbanStep(cardID, map[string]any{"name": "Preparing Agent", "status": "running", "model": model})

	// 3. 选择执行策略
	var result string
	if provider == "openai" {
		// OpenAI 支持 tool calling → 使用 Agent Loop，Skills 作为可调用工具
		log.Printf("🤖 [wish:%s] Using Agent Loop (tool calling enabled)", cardID[:8])
		result, err = s.executeWithAgent(card, installedSkills, apiKey, model, provider, userID, updater)
	} else {
		// Claude/Gemini → 回退到 prompt 增强模式
		log.Printf("📝 [wish:%s] Using direct mode (no tool calling)", cardID[:8])
		skillDescriptions := buildSkillDescriptions(installedSkills)
		if len(installedSkills) > 0 {
			result, err = s.executeWithSkillAwareness(card, installedSkills, skillDescriptions, apiKey, model, provider, userID, updater)
		} else {
			result, err = s.executeDirectly(card, apiKey, model, provider, updater)
		}
	}

	if err != nil {
		log.Printf("❌ [wish:%s] Execution failed: %v", cardID[:8], err)
		updater.UpdateKanbanCardBySystem(cardID, "failed", 0, fmt.Sprintf("Execution failed: %v", err))
		return
	}

	// 4. 完成
	log.Printf("✅ [wish:%s] Wish completed", cardID[:8])
	updater.UpdateKanbanCardBySystem(cardID, "done", 100, result)
}

// executeWithSkillAwareness 有 Skills 时的执行策略
func (s *Server) executeWithSkillAwareness(card *storage.KanbanCardRecord, skills []*storage.SkillRecord, skillDescriptions, apiKey, model, provider, userID string, updater mcp.KanbanUpdater) (string, error) {
	cardID := card.ID

	// 构建 system prompt
	systemPrompt := fmt.Sprintf(`You are a helpful AI agent. The user has made a wish (request) and you need to fulfill it.

## Available Skills
The following skills are installed and available to help:

%s

## Instructions
1. Analyze the user's wish carefully
2. If any installed skills can help, describe how you would use them
3. Then fulfill the wish to the best of your ability
4. Provide a clear, actionable result

Be concise and practical. Focus on delivering value.

Current time: %s`, skillDescriptions, time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))

	prompt := card.Title
	if card.Description != "" {
		prompt += "\n\n" + card.Description
	}

	updater.UpdateKanbanCardBySystem(cardID, "working", 50, "")

	// 调用 LLM
	result, err := callLLM(systemPrompt, prompt, apiKey, model, provider)
	if err != nil {
		return "", err
	}

	updater.UpdateKanbanCardBySystem(cardID, "working", 90, "")
	return result, nil
}

// executeDirectly 没有 Skills 时直接执行
func (s *Server) executeDirectly(card *storage.KanbanCardRecord, apiKey, model, provider string, updater mcp.KanbanUpdater) (string, error) {
	systemPrompt := "You are a helpful AI agent. The user has made a wish (request) and you need to fulfill it.\n\nProvide a clear, actionable result. Be concise and practical.\n\nCurrent time: " + time.Now().Format("2006-01-02 15:04:05 MST (Monday)")

	prompt := card.Title
	if card.Description != "" {
		prompt += "\n\n" + card.Description
	}

	updater.UpdateKanbanCardBySystem(card.ID, "working", 50, "")

	result, err := callLLM(systemPrompt, prompt, apiKey, model, provider)
	if err != nil {
		return "", err
	}

	updater.UpdateKanbanCardBySystem(card.ID, "working", 90, "")
	return result, nil
}

// executeWithAgent 使用 Agent Loop (ReAct tool calling) 执行 Wish
// Skills 被注册为可调用的工具，LLM 可以决定是否调用
func (s *Server) executeWithAgent(card *storage.KanbanCardRecord, installedSkills []*storage.SkillRecord, apiKey, model, provider, userID string, updater mcp.KanbanUpdater) (string, error) {
	cardID := card.ID

	// 1. Tool Selection: if too many skills, use LLM to pick relevant ones
	const maxDirectSkills = 20 // Below this threshold, just use all skills directly
	selectedSkills := installedSkills
	if len(installedSkills) > maxDirectSkills {
		log.Printf("🔍 [wish:%s] %d skills installed, running tool selection...", cardID[:8], len(installedSkills))
		updater.AppendKanbanStep(cardID, map[string]any{"name": "Selecting Tools", "status": "running"})

		selected, err := selectRelevantSkills(card.Title, installedSkills, apiKey, model, provider)
		if err != nil {
			log.Printf("⚠️ [wish:%s] Tool selection failed: %v, using first %d skills", cardID[:8], err, maxDirectSkills)
			selectedSkills = installedSkills[:maxDirectSkills]
		} else {
			selectedSkills = selected
			log.Printf("✅ [wish:%s] Selected %d relevant skills", cardID[:8], len(selectedSkills))
		}
		var selectedNames []string
		for _, sk := range selectedSkills {
			selectedNames = append(selectedNames, sk.Name)
		}
		updater.UpdateKanbanStep(cardID, "Selecting Tools", "done",
			fmt.Sprintf("Selected %d/%d: %s", len(selectedSkills), len(installedSkills), strings.Join(selectedNames, ", ")), 0)
	}

	// 2. 构建 Skill 工具列表（带脚本目录路径）
	localStore := skills.NewLocalStore(s.config.HomeDir)
	var skillTools []mcp.SkillTool
	for _, skill := range selectedSkills {
		st := mcp.SkillTool{
			ID:           skill.ID,
			Name:         skill.Name,
			Description:  skill.Description,
			Instructions: skill.Instructions,
		}
		// 如果 skill 有脚本，传入磁盘绝对路径（用于创建 symlink）
		if skill.HasScripts {
			skillDir := localStore.SkillDir(skill.Name)
			if abs, err := filepath.Abs(skillDir); err == nil {
				skillDir = abs
			}
			st.SkillDir = skillDir
		}
		skillTools = append(skillTools, st)
	}

	// 2b. Resolve skill secrets → env vars + pre-flight validation
	secretEnv := make(map[string]string)
	var missingSecrets []string
	for _, skill := range selectedSkills {
		for _, secretName := range skill.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue // already resolved
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				log.Printf("[wish:%s] secret %q for skill %q not found", cardID[:8], secretName, skill.Name)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s' requires secret '%s'", skill.Name, secretName))
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
				log.Printf("[wish:%s] decrypt secret %q failed: %v", cardID[:8], secretName, err)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s': failed to decrypt secret '%s'", skill.Name, secretName))
				continue
			}
			secretEnv[secretName] = val
		}
	}

	// Pre-flight: if critical secrets are missing, fail early with clear message
	if len(missingSecrets) > 0 {
		errMsg := "Missing required secrets:\n"
		for _, ms := range missingSecrets {
			errMsg += "• " + ms + "\n"
		}
		errMsg += "\nPlease configure them in Settings → Secrets."
		log.Printf("⚠️ [wish:%s] %s", cardID[:8], errMsg)
		return "", fmt.Errorf("%s", errMsg)
	}

	// 3. 构建额外内置工具
	// App 运行时不添加 search_skills/suggest_install（只用已配置的 skills）
	appID := card.AppID
	if appID == "" {
		appID = card.AgentID // legacy fallback
	}
	isAppRun := appID != ""
	var extraTools []mcp.ExtraBuiltinTool
	if !isAppRun {
		extraTools = s.buildWishTools(userID, cardID)
	}

	// 3a. 注入 App Capabilities (MCP servers, web_search, notify) + custom system prompt
	var capMCPServers []mcp.MCPServerConfig
	var appSystemPrompt string
	if appID != "" {
		if appRec, err := s.db.GetApp(appID); err == nil {
			appSystemPrompt = appRec.SystemPrompt
			caps, err := capability.Parse(appRec.Capabilities)
			if err != nil {
				log.Printf("⚠️ [wish:%s] Invalid capabilities JSON: %v", cardID[:8], err)
			}
			if caps != nil {
				secretGetter := func(name string) (string, error) {
					rec, err := s.db.GetSecret(userID, name)
					if err != nil {
						return "", err
					}
					return crypto.Decrypt(rec.EncryptedValue)
				}
				if err := capability.ResolveSecrets(caps, secretGetter); err != nil {
					log.Printf("⚠️ [wish:%s] Failed to resolve secrets: %v", cardID[:8], err)
				}
				capMCPServers = capability.BuildMCPServers(caps)

				// Web Search: inject the full web-search skill (not the bare API tool)
				if caps.WebSearch != nil && caps.WebSearch.Enabled {
					if wsSkill, err := s.db.GetSkillByName(userID, "web-search"); err == nil {
						// Check not already in skillTools
						alreadyHas := false
						for _, st := range skillTools {
							if st.Name == "web-search" {
								alreadyHas = true
								break
							}
						}
						if !alreadyHas {
							st := mcp.SkillTool{
								ID: wsSkill.ID, Name: wsSkill.Name,
								Description: wsSkill.Description, Instructions: wsSkill.Instructions,
							}
							if wsSkill.HasScripts {
								skillDir := localStore.SkillDir(wsSkill.Name)
								if abs, err := filepath.Abs(skillDir); err == nil {
									skillDir = abs
								}
								st.SkillDir = skillDir
							}
							skillTools = append(skillTools, st)
							// Resolve BRAVE_API_KEY for the skill
							for _, sn := range wsSkill.RequiredSecretsList() {
								if _, ok := secretEnv[sn]; !ok {
									if rec, err := s.db.GetSecret(userID, sn); err == nil {
										if val, err := crypto.Decrypt(rec.EncryptedValue); err == nil {
											secretEnv[sn] = val
										}
									}
								}
							}
							log.Printf("🌐 [wish:%s] Web Search: injected web-search skill", cardID[:8])
						}
					} else {
						log.Printf("⚠️ [wish:%s] Web Search enabled but web-search skill not found, falling back to API tool", cardID[:8])
						if apiKey, err := secretGetter("BRAVE_API_KEY"); err == nil && apiKey != "" {
							extraTools = append(extraTools, capability.BuildWebSearchTool(apiKey))
						}
					}
				}

				// Other capability tools (notify, etc.) — skip web_search since handled above
				capTools := capability.BuildNonSearchTools(caps, secretGetter)
				extraTools = append(extraTools, capTools...)

				if len(capMCPServers) > 0 {
					log.Printf("🔌 [wish:%s] Injecting %d MCP servers from capabilities", cardID[:8], len(capMCPServers))
				}
			}
		}
	}

	// 4. System Prompt — App runs get a focused prompt (no skill discovery)
	var systemPrompt string
	if isAppRun {
		systemPrompt = `You are Tofi, an autonomous AI agent executing a scheduled App task. Fulfill the task by TAKING ACTION, not just giving advice.

## Core Principle: ACT, Don't Advise
You are an EXECUTOR, not an advisor. When a task requires running commands, you MUST run them using sandbox_exec. Never just describe what commands to run — execute them yourself and report the results.

## Tools Available
- **run_skill_***: Invoke a configured skill. Skills return data or suggest commands. **If a skill returns commands, you MUST execute them with sandbox_exec.**
- **sandbox_exec**: Run shell commands in a sandbox (python3, node, curl, git, jq, npm, etc.).
- **update_kanban**: Report progress as you work.

You can ONLY use the tools listed above. Do NOT attempt to search for or install new skills.

## Sandbox Environment
You have a sandbox shell (macOS) with full system tools available. Package installs persist across tasks.
- **Python packages**: ALWAYS use ` + "`python3 -m pip install <pkg>`" + ` (NEVER bare "pip")
- **Multi-line Python**: ALWAYS use heredoc syntax:
  ` + "```" + `
  python3 <<'PYEOF'
  import yfinance as yf
  data = yf.download("AAPL", period="5d")
  print(data)
  PYEOF
  ` + "```" + `
  NEVER cram complex Python into a single python3 -c "..." line.
- If a command is not found, install it with python3 -m pip or npm
- ALWAYS execute commands and return real results

## Workflow
1. Analyze the task
2. Use configured skills and sandbox_exec to get things done
3. **Execute commands ONE AT A TIME** — call sandbox_exec once, check the result, then decide next.
4. If a command fails, adapt and retry with a different approach (max 3 retries per command)
5. Provide actual results, not just suggestions

## CRITICAL: Never Give Up
- **NEVER respond with "go do it yourself".** You are an executor — if one approach fails, try another.
- **Always deliver SOMETHING useful.** Present partial results if needed.
- **Fallback chain**: skill → fix command → write own code → alternative data source → partial results.

## Language
Always respond in the same language as the task description. If in Chinese, respond in Chinese.

Current time: ` + time.Now().Format("2006-01-02 15:04:05 MST (Monday)")
	} else {
		systemPrompt = `You are Tofi, an autonomous AI agent. The user has made a wish (request) and you need to fulfill it by TAKING ACTION, not just giving advice.

## Core Principle: ACT, Don't Advise
You are an EXECUTOR, not an advisor. When a task requires running commands, you MUST run them using sandbox_exec. Never just describe what commands to run — execute them yourself and report the results.

## Tools Available
- **run_skill_***: Invoke an installed skill. Skills return data or suggest commands. **If a skill returns commands, you MUST execute them with sandbox_exec — never just relay the skill's instructions back to the user.**
- **sandbox_exec**: Run shell commands in a sandbox. Full system tools available (python3, node, curl, git, jq, npm, etc.). This is your primary tool for getting things done.
- **search_skills**: Find new skills on the skills.sh marketplace.
- **suggest_install**: Suggest installing a skill — execution will PAUSE for user approval.
- **update_kanban**: Report progress as you work.

## Sandbox Environment
You have a sandbox shell (macOS) with full system tools available. Package installs persist across tasks.
- **Python packages**: ALWAYS use ` + "`python3 -m pip install <pkg>`" + ` (NEVER bare "pip" — it does not exist on macOS, only pip3)
- **Node packages**: use npm install
- **Multi-line Python**: For anything beyond a trivial one-liner, ALWAYS use heredoc syntax:
  ` + "```" + `
  python3 <<'PYEOF'
  import yfinance as yf
  data = yf.download("AAPL", period="5d")
  print(data)
  PYEOF
  ` + "```" + `
  NEVER cram complex Python into a single python3 -c "..." line — it causes shell quoting and syntax errors.
- If a command is not found, install it with python3 -m pip or npm
- If a skill suggests a CLI tool that doesn't exist, use the underlying library directly
- ALWAYS execute commands and return real results — never tell the user to "run this command themselves"

## Skill Installation
When you find a useful skill via search_skills that isn't installed yet:
1. Call suggest_install with the skill_id, skill_name, and reason
2. Execution will PAUSE — the user will see Install and Skip buttons
3. If installed, the skill becomes available immediately

## Workflow
1. Analyze the user's wish
2. Use skills and sandbox_exec to get things done
3. **Execute commands ONE AT A TIME** — call sandbox_exec once, check the result, then decide the next command. Never batch multiple sandbox_exec calls in parallel.
4. If a command fails, adapt and retry with a different approach (max 3 retries per command)
5. **If a skill returns commands that fail, do NOT keep calling more skills hoping for better results.** Instead, write your own commands using sandbox_exec directly (curl + sed/grep for web, python3 with heredoc for scripting).
6. Provide actual results, not just suggestions

## CRITICAL: Never Give Up
- **NEVER respond with "go do it yourself" or "visit website X manually".** You are an executor — if one approach fails, try another.
- **Always deliver SOMETHING useful.** If you got partial data (e.g. 1 out of 3 sources worked), present what you have and clearly state what failed.
- **When a skill's commands fail, write your OWN code.** Don't just skip to the next task — debug, simplify, and retry.
- **Fallback chain**: skill command → fix the command → write simpler code yourself → try alternative data source → present partial results. NEVER end with "I couldn't do it."

## Language
Always respond in the same language as the user's wish. If the user writes in Chinese, respond in Chinese. If in English, respond in English.

Current time: ` + time.Now().Format("2006-01-02 15:04:05 MST (Monday)")
	}

	// Inject app's custom system prompt if present
	if appSystemPrompt != "" {
		systemPrompt = systemPrompt + "\n\n## App Instructions\n" + appSystemPrompt
	}

	prompt := card.Title
	if card.Description != "" {
		prompt += "\n\n" + card.Description
	}

	updater.UpdateKanbanCardBySystem(cardID, "working", 30, "")
	updater.UpdateKanbanStep(cardID, "Preparing Agent", "done", "", 0)

	// 4. 创建沙箱（使用 Executor 接口，支持 Direct/Docker 模式）
	sandboxDir, err := s.executor.CreateSandbox(executor.SandboxConfig{
		HomeDir: s.config.HomeDir,
		UserID:  userID,
		CardID:  cardID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create sandbox: %v", err)
	}
	defer s.executor.Cleanup(sandboxDir)
	log.Printf("📦 [wish:%s] Sandbox created: %s", cardID[:8], sandboxDir)

	// 6. 构建 Agent Config
	agentCfg := mcp.AgentConfig{
		System:        systemPrompt,
		Prompt:        prompt,
		MCPServers:    capMCPServers,
		SkillTools:    skillTools,
		ExtraTools:    append(extraTools, s.buildMemoryTools(userID, cardID)...),
		KanbanCardID:  cardID,
		KanbanUpdater: updater,
		SandboxDir:    sandboxDir,
		UserDir:       userID, // Docker mode uses this to identify container
		Executor:      s.executor,
		SecretEnv:     secretEnv,
		OnStreamChunk: func(cardID, delta string) {
			s.sseHub.Publish(CardEvent{
				Type:   "result_chunk",
				CardID: cardID,
				Data:   map[string]any{"delta": delta},
			})
		},
		OnContextCompact: func(summary string, originalTokens, compactedTokens int) {
			if updater != nil {
				stepData := map[string]interface{}{
					"name":             "Context Compressed",
					"status":           "done",
					"input_tokens":     originalTokens,
					"compacted_tokens": compactedTokens,
				}
				updater.AppendKanbanStep(cardID, stepData)
			}
		},
	}
	agentCfg.AI.Model = model
	agentCfg.AI.APIKey = apiKey
	agentCfg.AI.Provider = provider
	agentCfg.AI.Endpoint = "https://api.openai.com/v1/chat/completions"

	// 6. 创建执行上下文
	ctx := models.NewExecutionContext(cardID[:8], userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	// 7. 运行 Agent Loop
	result, err := mcp.RunAgentLoop(agentCfg, ctx)
	if err != nil {
		return "", err
	}

	updater.UpdateKanbanCardBySystem(cardID, "working", 90, "")
	return result, nil
}

// buildWishTools 构建 Wish 执行时的额外内置工具
func (s *Server) buildWishTools(userID, cardID string) []mcp.ExtraBuiltinTool {
	return []mcp.ExtraBuiltinTool{
		{
			Schema: mcp.OpenAITool{
				Type: "function",
				Function: mcp.OpenAIFunctionDef{
					Name:        "search_skills",
					Description: "Search for skills on the skills.sh marketplace. Use this when you need a capability that isn't already installed.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"query": map[string]interface{}{
								"type":        "string",
								"description": "Search query (e.g., 'react testing', 'code review', 'summarize')",
							},
						},
						"required": []string{"query"},
					},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				query, _ := args["query"].(string)
				if query == "" {
					return "Error: query is required", nil
				}
				client := skills.NewRegistryClient("")
				result, err := client.Search(query, 5)
				if err != nil {
					return fmt.Sprintf("Search failed: %v", err), nil
				}
				if len(result.Skills) == 0 {
					return "No skills found for query: " + query, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d skills:\n\n", len(result.Skills)))
				for _, sk := range result.Skills {
					sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", sk.Name, sk.Source, sk.Description))
					sb.WriteString(fmt.Sprintf("  Install: use suggest_install with skill_id=\"%s\"\n", sk.ID))
				}
				return sb.String(), nil
			},
		},
		{
			Schema: mcp.OpenAITool{
				Type: "function",
				Function: mcp.OpenAIFunctionDef{
					Name: "suggest_install",
					Description: "Suggest installing a skill. Execution will PAUSE until the user installs and clicks Continue, or skips. " +
						"Use this after search_skills finds a useful skill that isn't installed yet.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"skill_id": map[string]interface{}{
								"type":        "string",
								"description": "Full skill ID from search results (e.g., 'owner/repo@skill-name')",
							},
							"skill_name": map[string]interface{}{
								"type":        "string",
								"description": "Human-readable skill name",
							},
							"reason": map[string]interface{}{
								"type":        "string",
								"description": "Why this skill would be useful for the current task",
							},
						},
						"required": []string{"skill_id", "skill_name"},
					},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				skillID, _ := args["skill_id"].(string)
				skillName, _ := args["skill_name"].(string)
				reason, _ := args["reason"].(string)

				if skillID == "" || skillName == "" {
					return "Error: skill_id and skill_name are required", nil
				}

				// 1. Append action (status: pending)
				action := storage.KanbanAction{
					Type:    "install_skill",
					SkillID: skillID,
					Name:    skillName,
					Reason:  reason,
					Status:  "pending",
				}
				if err := s.db.AppendKanbanAction(cardID, action); err != nil {
					return fmt.Sprintf("Failed to suggest installation: %v", err), nil
				}

				// 2. Set card to "hold" — pauses agent execution
				s.db.UpdateKanbanCardStatus(cardID, "hold")
				log.Printf("⏸ [wish:%s] Agent paused — waiting for user action on skill: %s", cardID[:8], skillName)

				// 3. Block until user clicks Continue/Skip or timeout
				holdCh := s.createHoldChannel(cardID)
				timeout := time.After(10 * time.Minute)

				select {
				case signal := <-holdCh:
					if signal.Action == "abort" {
						log.Printf("⏭ [wish:%s] User skipped skill install, resuming agent", cardID[:8])
						s.db.UpdateKanbanCardStatus(cardID, "working")
						return fmt.Sprintf("User chose to skip installing '%s'. Continue without it.", skillName), nil
					}
					// "continue" — user installed and clicked Continue
					log.Printf("▶ [wish:%s] User continued after skill install, resuming agent", cardID[:8])
					s.db.UpdateKanbanCardStatus(cardID, "working")
					return fmt.Sprintf("Skill '%s' has been installed successfully and is now available. You can use it to complete the task.", skillName), nil

				case <-timeout:
					log.Printf("⏰ [wish:%s] Hold timed out after 10 minutes, auto-skipping", cardID[:8])
					s.removeHoldChannel(cardID)
					s.db.UpdateKanbanCardStatus(cardID, "working")
					// Mark action as aborted
					actions, _ := s.db.GetKanbanActions(cardID)
					for i := len(actions) - 1; i >= 0; i-- {
						if actions[i].SkillID == skillID && actions[i].Status == "pending" {
							s.db.UpdateKanbanAction(cardID, i, "aborted", "Timed out after 10 minutes")
							break
						}
					}
					return fmt.Sprintf("Installation of '%s' timed out. Continuing without it.", skillName), nil
				}
			},
		},
	}
}

// callLLM 统一调用 LLM API (用于非 OpenAI 的 fallback 模式)
func callLLM(system, prompt, apiKey, model, provider string) (string, error) {
	headers := make(map[string]string)
	var payload map[string]interface{}
	var endpoint string

	switch provider {
	case "claude", "anthropic":
		endpoint = "https://api.anthropic.com/v1/messages"
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		payload = map[string]interface{}{
			"model":      model,
			"system":     system,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
			"max_tokens": 4096,
		}
	case "gemini":
		endpoint = fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
		headers["x-goog-api-key"] = apiKey
		payload = map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{
					"parts": []map[string]string{{"text": system + "\n\n" + prompt}},
				},
			},
		}
	default: // OpenAI
		endpoint = "https://api.openai.com/v1/chat/completions"
		headers["Authorization"] = "Bearer " + apiKey
		payload = map[string]interface{}{
			"model": model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": prompt},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_ = ctx // executor.PostJSON uses its own timeout

	resp, err := executor.PostJSON(endpoint, headers, payload, 120)
	if err != nil {
		return "", fmt.Errorf("LLM API call failed: %v", err)
	}

	// Parse response
	paths := []string{
		"content.0.text",                                    // Claude
		"output.#(type==\"message\").content.0.text",        // OpenAI Responses API
		"choices.0.message.content",                         // OpenAI Chat
		"candidates.0.content.parts.0.text",                 // Gemini
	}
	for _, path := range paths {
		if res := gjson.Get(resp, path); res.Exists() {
			return res.String(), nil
		}
	}

	return "", fmt.Errorf("failed to parse LLM response")
}

// selectRelevantSkills uses a lightweight LLM call to pick the most relevant skills for a wish.
// Sends all skill names+descriptions, asks LLM to return relevant skill names.
func selectRelevantSkills(wish string, allSkills []*storage.SkillRecord, apiKey, model, provider string) ([]*storage.SkillRecord, error) {
	// Build compact skill catalog: "name: description" per line
	var catalog strings.Builder
	nameIndex := make(map[string]*storage.SkillRecord)
	for _, s := range allSkills {
		desc := s.Description
		if len(desc) > 100 {
			desc = desc[:100] + "..."
		}
		catalog.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, desc))
		nameIndex[strings.ToLower(s.Name)] = s
	}

	system := `You are a tool selector. Given a user request and a list of available skills, return ONLY the names of skills that are relevant to fulfilling the request. Return one skill name per line, nothing else. Pick at most 10 skills. If none are relevant, return "NONE".`

	prompt := fmt.Sprintf("User request: %s\n\nAvailable skills (%d total):\n%s", wish, len(allSkills), catalog.String())

	resp, err := callLLM(system, prompt, apiKey, model, provider)
	if err != nil {
		return nil, fmt.Errorf("selection LLM call failed: %v", err)
	}

	// Parse response: each line is a skill name
	var selected []*storage.SkillRecord
	for _, line := range strings.Split(resp, "\n") {
		name := strings.TrimSpace(line)
		name = strings.TrimPrefix(name, "- ")
		name = strings.TrimPrefix(name, "* ")
		if name == "" || strings.EqualFold(name, "NONE") {
			continue
		}
		// Fuzzy match: try exact, then lowercase
		if s, ok := nameIndex[strings.ToLower(name)]; ok {
			selected = append(selected, s)
		}
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("no skills selected from response")
	}
	return selected, nil
}

// buildSkillDescriptions 构建 Skill 描述列表
func buildSkillDescriptions(skills []*storage.SkillRecord) string {
	if len(skills) == 0 {
		return "(No skills installed)"
	}

	var parts []string
	for _, s := range skills {
		desc := fmt.Sprintf("- **%s**: %s", s.Name, s.Description)
		if s.Source == "git" && s.SourceURL != "" {
			desc += fmt.Sprintf(" (from %s)", s.SourceURL)
		}
		parts = append(parts, desc)
	}
	return strings.Join(parts, "\n")
}

// detectProvider 从模型名推断 provider
func detectProvider(model string) string {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "claude") {
		return "claude"
	}
	if strings.HasPrefix(m, "gemini") {
		return "gemini"
	}
	return "openai"
}
