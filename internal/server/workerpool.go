package server

import (
	"context"
	"log"
	"sync"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/storage"
)

// WorkflowJob 表示一个待执行的工作流任务
type WorkflowJob struct {
	ExecutionID   string
	Workflow      *models.Workflow
	Context       *models.ExecutionContext
	InitialInputs map[string]interface{}
	DB            *storage.DB
}

// WorkerPool 管理工作流执行的并发池
type WorkerPool struct {
	maxWorkers   int                        // 最大并发数
	jobQueue     chan *WorkflowJob          // 任务队列（无缓冲，阻塞式）
	registry     *ExecutionRegistry         // 执行上下文注册表
	ctx          context.Context            // 用于优雅关闭
	cancel       context.CancelFunc         // 取消函数
	wg           sync.WaitGroup             // 等待所有 worker 退出
	queuedCount  int64                      // 排队任务数
	runningCount int64                      // 运行中任务数
	mu           sync.RWMutex               // 保护计数器
}

// NewWorkerPool 创建一个新的工作池
func NewWorkerPool(maxWorkers int, registry *ExecutionRegistry) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		maxWorkers: maxWorkers,
		jobQueue:   make(chan *WorkflowJob, 100), // 队列缓冲 100 个任务
		registry:   registry,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start 启动工作池的所有 worker
func (wp *WorkerPool) Start() {
	log.Printf("🏊 启动工作池，最大并发数: %d", wp.maxWorkers)

	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// worker 是一个长期运行的 goroutine，从队列中获取任务并执行
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	log.Printf("Worker #%d 已启动", id)

	for {
		select {
		case <-wp.ctx.Done():
			log.Printf("Worker #%d 正在关闭...", id)
			return

		case job := <-wp.jobQueue:
			if job == nil {
				continue
			}

			wp.decrementQueued()
			wp.incrementRunning()

			log.Printf("Worker #%d 开始执行任务: %s", id, job.ExecutionID)
			wp.executeJob(job)

			wp.decrementRunning()
			log.Printf("Worker #%d 完成任务: %s", id, job.ExecutionID)
		}
	}
}

// executeJob 执行单个工作流任务
func (wp *WorkerPool) executeJob(job *WorkflowJob) {
	defer wp.registry.Unregister(job.ExecutionID)
	defer job.Context.Close()

	defer func() {
		if r := recover(); r != nil {
			job.Context.Log("PANIC RECOVERED: %v", r)
		}
	}()

	job.Context.Log("🚀 Execution Started (Worker Pool)")
	engine.Start(job.Workflow, job.Context, job.InitialInputs)
	job.Context.Wg.Wait()

	engine.Cleanup(job.Context)

	if err := engine.SaveReport(job.Workflow, job.Context, job.DB); err != nil {
		job.Context.Log("Failed to save report to DB: %v", err)
	} else {
		job.Context.Log("Execution record saved to database")
	}

	job.Context.Log("🏁 Execution Finished")
}

// Submit 提交一个新任务到工作池
// 如果队列已满，会阻塞直到有空间
func (wp *WorkerPool) Submit(job *WorkflowJob) error {
	wp.registry.Register(job.ExecutionID, job.Context)

	wp.incrementQueued()

	select {
	case <-wp.ctx.Done():
		wp.registry.Unregister(job.ExecutionID)
		wp.decrementQueued()
		return context.Canceled

	case wp.jobQueue <- job:
		log.Printf("任务 %s 已加入队列 (队列长度: %d)", job.ExecutionID, wp.GetQueuedCount())
		return nil
	}
}

// Shutdown 优雅关闭工作池
func (wp *WorkerPool) Shutdown() {
	log.Println("🛑 正在关闭工作池...")

	wp.cancel()
	close(wp.jobQueue)
	wp.wg.Wait()

	log.Println("✅ 工作池已关闭")
}

// GetQueuedCount 返回队列中等待的任务数
func (wp *WorkerPool) GetQueuedCount() int64 {
	wp.mu.RLock()
	defer wp.mu.RUnlock()
	return wp.queuedCount
}

// GetRunningCount 返回正在运行的任务数
func (wp *WorkerPool) GetRunningCount() int64 {
	wp.mu.RLock()
	defer wp.mu.RUnlock()
	return wp.runningCount
}

// GetStats 返回工作池的统计信息
func (wp *WorkerPool) GetStats() map[string]interface{} {
	wp.mu.RLock()
	defer wp.mu.RUnlock()

	return map[string]interface{}{
		"max_workers":    wp.maxWorkers,
		"queued_jobs":    wp.queuedCount,
		"running_jobs":   wp.runningCount,
		"queue_capacity": cap(wp.jobQueue),
		"queue_length":   len(wp.jobQueue),
	}
}

func (wp *WorkerPool) incrementQueued() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.queuedCount++
}

func (wp *WorkerPool) decrementQueued() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.queuedCount--
}

func (wp *WorkerPool) incrementRunning() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.runningCount++
}

func (wp *WorkerPool) decrementRunning() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.runningCount--
}
