package logic

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"tofi-core/internal/models"
)

type Loop struct{}

func (l *Loop) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	mode := fmt.Sprint(config["mode"])
	iterator := fmt.Sprint(config["iterator"])
	if iterator == "" {
		iterator = "item"
	}

	var items []interface{}
	var err error
	switch mode {
	case "list":
		items, err = l.parseListItems(config)
	case "range":
		items, err = l.generateRangeItems(config)
	default:
		return "", fmt.Errorf("unsupported loop mode: %s", mode)
	}
	if err != nil {
		return "", err
	}

	taskTemplate, ok := config["task"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("config.task must be an object")
	}

	maxConcurrency := 1
	if concVal := config["max_concurrency"]; concVal != nil {
		maxConcurrency, _ = strconv.Atoi(fmt.Sprint(concVal))
		if maxConcurrency <= 0 {
			maxConcurrency = len(items)
		}
	}

	failFast := fmt.Sprint(config["fail_fast"]) == "true"

	return l.executeLoop(items, iterator, taskTemplate, ctx, maxConcurrency, failFast)
}

func (l *Loop) parseListItems(config map[string]interface{}) ([]interface{}, error) {
	rawList := config["items"]
	var items []interface{}
	switch v := rawList.(type) {
	case []interface{}:
		items = v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("items is empty")
		}
		if err := json.Unmarshal([]byte(v), &items); err != nil {
			return nil, fmt.Errorf("failed to parse items JSON: %v", err)
		}
	default:
		return nil, fmt.Errorf("items must be an array or JSON string")
	}
	return items, nil
}

func (l *Loop) generateRangeItems(config map[string]interface{}) ([]interface{}, error) {
	start, err := strconv.Atoi(fmt.Sprint(config["start"]))
	if err != nil {
		return nil, fmt.Errorf("invalid start: %v", err)
	}
	end, err := strconv.Atoi(fmt.Sprint(config["end"]))
	if err != nil {
		return nil, fmt.Errorf("invalid end: %v", err)
	}
	step := 1
	if sVal := config["step"]; sVal != nil {
		step, _ = strconv.Atoi(fmt.Sprint(sVal))
		if step == 0 {
			step = 1
		}
	}
	var items []interface{}
	if step > 0 {
		for i := start; i <= end; i += step {
			items = append(items, i)
		}
	} else {
		for i := start; i >= end; i += step {
			items = append(items, i)
		}
	}
	return items, nil
}

func (l *Loop) executeLoop(
	items []interface{},
	iterator string,
	taskTemplate map[string]interface{},
	ctx *models.ExecutionContext,
	maxConcurrency int,
	failFast bool,
) (string, error) {
	results := make([]interface{}, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstError error
	semaphore := make(chan struct{}, maxConcurrency)

	for idx, item := range items {
		if failFast && firstError != nil {
			break
		}
		wg.Add(1)
		semaphore <- struct{}{}

		go func(i int, val interface{}) {
			defer wg.Done()
			defer func() { <-semaphore }()

			if failFast && firstError != nil {
				return
			}

			// 使用 Derive 创建隔离的子上下文
			iterationID := fmt.Sprintf("%d", i+1)
			childCtx := ctx.Derive(iterationID)

			// 确保子产物目录存在
			os.MkdirAll(childCtx.Paths.Artifacts, 0755)
			os.MkdirAll(childCtx.Paths.Uploads, 0755)

			var itemValue string
			switch v := val.(type) {
			case string:
				itemValue = v
			case int, int64, float64, bool:
				itemValue = fmt.Sprint(v)
			default:
				itemJSON, _ := json.Marshal(val)
				itemValue = string(itemJSON)
			}
			childCtx.SetResult(iterator, itemValue)

			// 解析子节点任务
			nodeType := fmt.Sprint(taskTemplate["type"])
			mockNode := &models.Node{
				Type: nodeType,
				ID:   fmt.Sprintf("loop_item_%d", i+1),
			}

			// 处理 Input Template
			if inputRaw, ok := taskTemplate["input"]; ok {
				// 我们需要将 input 转换回 []Parameter
				// 因为是从 YAML/JSON 解析出来的，它可能是 []interface{}
				jb, _ := json.Marshal(inputRaw)
				json.Unmarshal(jb, &mockNode.Input)
			}

			// 处理 Config Template
			if configRaw, ok := taskTemplate["config"].(map[string]interface{}); ok {
				mockNode.Config = configRaw
			}

			// 执行两阶段解析
			localCtx, err := models.ResolveLocalContext(mockNode, childCtx)
			if err != nil {
				l.recordError(&mu, &results, &firstError, i, err, failFast)
				return
			}
			resolvedConfig, err := models.ResolveConfig(mockNode.Config, localCtx, childCtx)
			if err != nil {
				l.recordError(&mu, &results, &firstError, i, err, failFast)
				return
			}

			action := getActionForLoop(nodeType)
			res, err := action.Execute(resolvedConfig, childCtx)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if failFast && firstError == nil {
					firstError = fmt.Errorf("iteration %d failed: %v", i, err)
				}
				results = append(results, map[string]interface{}{"index": i, "error": err.Error()})
			} else {
				var jsonRes interface{}
				if json.Unmarshal([]byte(res), &jsonRes) == nil {
					results = append(results, jsonRes)
				} else {
					results = append(results, res)
				}
			}
		}(idx, item)
	}

	wg.Wait()
	if failFast && firstError != nil {
		return "", firstError
	}
	resJSON, _ := json.Marshal(results)
	return string(resJSON), nil
}

func (l *Loop) recordError(mu *sync.Mutex, results *[]interface{}, firstErr *error, i int, err error, failFast bool) {
	mu.Lock()
	defer mu.Unlock()
	if failFast && *firstErr == nil {
		*firstErr = fmt.Errorf("iteration %d failed during resolution: %v", i, err)
	}
	*results = append(*results, map[string]interface{}{"index": i, "error": err.Error()})
}

var actionGetter func(string) Action

type Action interface {
	Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error)
	Validate(node *models.Node) error
}

func SetActionGetter(getter func(string) Action) {
	actionGetter = getter
}

func getActionForLoop(nodeType string) Action {
	return actionGetter(nodeType)
}

func (l *Loop) Validate(n *models.Node) error {
	return nil
}
