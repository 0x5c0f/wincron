package scheduler

import (
	"fmt"
	"sync"
	"time"

	"wincron/internal/config"
	"wincron/internal/cronexpr"
	"wincron/internal/executor"
	"wincron/internal/notify"

	"github.com/robfig/cron/v3"
)

// Scheduler Cron调度引擎
type Scheduler struct {
	cron     *cron.Cron
	jobs     map[string]cron.EntryID // jobID -> cron entry ID
	mu       sync.RWMutex
	config   *config.ConfigManager
	executor *executor.Executor
	notifier *notify.Notifier
	running  bool

	stopCh  chan struct{}  // 配置热加载协程的停止信号
	jobWg   sync.WaitGroup // 手动触发（RunOnce）的任务，Stop 时需等待
	runMu   sync.Mutex     // 保护 stopped 与 jobWg.Add，避免 Add/Wait 竞态
	stopped bool
}

// NewScheduler 创建调度器
func NewScheduler(cm *config.ConfigManager, exec *executor.Executor, notifier *notify.Notifier) *Scheduler {
	return &Scheduler{
		jobs:     make(map[string]cron.EntryID),
		config:   cm,
		executor: exec,
		notifier: notifier,
	}
}

// Start 启动调度器
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("调度器已在运行")
	}

	// 统一使用 6 字段解析器；cronexpr 会把用户写的 5 字段自动补 "0 " 成 6 字段，
	// 因此 enable_seconds 开关不再影响表达式写法，Linux crontab 习惯可直接照搬。
	cr := cron.New(cron.WithSeconds(), cron.WithLocation(time.Local))
	s.cron = cr
	s.jobs = make(map[string]cron.EntryID)
	s.stopCh = make(chan struct{})

	s.runMu.Lock()
	s.stopped = false
	s.runMu.Unlock()

	cfg := s.config.Get() // 深拷贝
	loaded := 0
	for i := range cfg.Jobs {
		job := cfg.Jobs[i]
		if !job.Enabled {
			continue
		}
		if err := s.addJobInternal(&job); err != nil {
			fmt.Printf("[WinCron] 加载任务 %s (%s) 失败: %v\n", job.Name, job.ID, err)
		} else {
			loaded++
		}
	}

	cr.Start()
	s.running = true

	// 热加载由 ConfigManager 的回调驱动：watchConfig 只负责检测文件变化，
	// 真正重建调度表统一走 Reload，避免出现两条互相冲突的重载路径。
	s.config.SetOnReload(func(_ *config.CrontabFile) {
		if err := s.Reload(); err != nil {
			fmt.Printf("[WinCron] 配置热加载失败: %v\n", err)
		}
	})
	go s.watchConfig(s.stopCh)

	fmt.Printf("[WinCron] 调度器已启动，已加载 %d 个任务（cron 自动兼容 5/6 字段）\n", loaded)
	return nil
}

// Stop 停止调度器：先停热加载，再等 cron 已分发的任务，最后等手动触发的任务
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}

	// 标记停止，防止 Stop 期间又通过 RunOnce 塞进新任务
	s.runMu.Lock()
	s.stopped = true
	s.runMu.Unlock()

	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done() // robfig/cron 的 jobWaiter 会等待已分发的 job 结束
	}
	s.jobWg.Wait()

	s.jobs = make(map[string]cron.EntryID)
	s.running = false
	fmt.Println("[WinCron] 调度器已停止")
}

// IsRunning 检查是否在运行
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// Reload 重新加载所有任务。
// 全程持有 s.mu：旧实现提前 Unlock 后再写 s.jobs，与 AddJob/RemoveJob 并发时
// 会触发 concurrent map writes，这是本次审查发现的 P0 缺陷。
func (s *Scheduler) Reload() error {
	cfg := s.config.Get() // 锁外取深拷贝，缩短持锁时间

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.cron == nil {
		return fmt.Errorf("调度器未运行")
	}

	for _, entryID := range s.jobs {
		s.cron.Remove(entryID)
	}
	s.jobs = make(map[string]cron.EntryID)

	for i := range cfg.Jobs {
		job := cfg.Jobs[i]
		if !job.Enabled {
			continue
		}
		if err := s.addJobInternal(&job); err != nil {
			fmt.Printf("[WinCron] 重新加载任务 %s (%s) 失败: %v\n", job.Name, job.ID, err)
		} else {
			fmt.Printf("[WinCron] 重新加载任务: %s (%s)\n", job.Name, job.ID)
		}
	}
	return nil
}

// AddJob 添加任务（同 ID 覆盖旧调度）
func (s *Scheduler) AddJob(job *config.JobConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.cron == nil {
		return fmt.Errorf("调度器未初始化")
	}

	if old, ok := s.jobs[job.ID]; ok {
		s.cron.Remove(old)
		delete(s.jobs, job.ID)
	}
	return s.addJobInternal(job)
}

// addJobInternal 内部添加任务（调用者必须持有 s.mu 写锁）
func (s *Scheduler) addJobInternal(job *config.JobConfig) error {
	if !job.Enabled {
		return nil
	}

	expr, err := cronexpr.Parse(job.Cron)
	if err != nil {
		return fmt.Errorf("任务 %s: %w", job.ID, err)
	}

	jobCopy := *job // 复制值，避免闭包捕获循环变量
	entryID, err := s.cron.AddFunc(expr, func() {
		s.runJob(&jobCopy)
	})
	if err != nil {
		return fmt.Errorf("添加任务 %s 到调度器失败: %w", job.ID, err)
	}

	s.jobs[job.ID] = entryID
	if expr != job.Cron {
		fmt.Printf("[WinCron] 添加任务: %s | cron: %q -> 归一化 %q | command: %s\n",
			job.Name, job.Cron, expr, job.Command)
	} else {
		fmt.Printf("[WinCron] 添加任务: %s | cron: %q | command: %s\n", job.Name, job.Cron, job.Command)
	}
	return nil
}

// RemoveJob 移除任务
func (s *Scheduler) RemoveJob(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cron == nil {
		return fmt.Errorf("调度器未初始化")
	}
	entryID, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("任务 %s 不在调度器中", jobID)
	}

	s.cron.Remove(entryID)
	delete(s.jobs, jobID)
	fmt.Printf("[WinCron] 移除任务: %s\n", jobID)
	return nil
}

// GetRunningJobs 获取已调度任务数量
func (s *Scheduler) GetRunningJobs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}

// ListScheduledJobs 列出已调度的任务
func (s *Scheduler) ListScheduledJobs() []ScheduledJobInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ScheduledJobInfo, 0, len(s.jobs))
	if s.cron == nil {
		return result
	}
	for jobID, entryID := range s.jobs {
		entry := s.cron.Entry(entryID)
		result = append(result, ScheduledJobInfo{
			JobID:   jobID,
			EntryID: int(entryID),
			Next:    entry.Next,
			Prev:    entry.Prev,
		})
	}
	return result
}

// ScheduledJobInfo 调度任务信息
type ScheduledJobInfo struct {
	JobID   string    `json:"job_id"`
	EntryID int       `json:"entry_id"`
	Next    time.Time `json:"next_run"`
	Prev    time.Time `json:"prev_run"`
}

// runJob 执行任务（被cron调度调用）
func (s *Scheduler) runJob(job *config.JobConfig) {
	fmt.Printf("[WinCron] 开始执行任务: %s (%s)\n", job.Name, job.ID)

	result := s.executor.Execute(job)

	if result.Success {
		fmt.Printf("[WinCron] 任务完成: %s | 耗时: %.2fs\n", job.Name, result.Duration)
	} else {
		fmt.Printf("[WinCron] 任务失败: %s | 退出码: %d | 重试次数: %d\n", job.Name, result.ExitCode, result.Retry)
	}

	if s.notifier != nil {
		s.notifier.Notify(job, result)
	}
}

// watchConfig 定时检查配置文件变更；检测本身不做调度重建，由 onReload 回调统一处理
func (s *Scheduler) watchConfig(stopCh <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if s.config.CheckReload() {
				fmt.Println("[WinCron] 检测到配置文件变更，已触发热加载")
			}
		}
	}
}

// RunOnce 手动执行一次任务
func (s *Scheduler) RunOnce(jobID string) error {
	job, err := s.config.GetJob(jobID)
	if err != nil {
		return err
	}

	s.runMu.Lock()
	if s.stopped {
		s.runMu.Unlock()
		return fmt.Errorf("调度器已停止，无法手动执行")
	}
	s.jobWg.Add(1)
	s.runMu.Unlock()

	go func() {
		defer s.jobWg.Done()
		s.runJob(job)
	}()
	return nil
}
