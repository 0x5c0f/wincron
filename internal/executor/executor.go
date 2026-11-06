package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"wincron/internal/config"
	"wincron/internal/paths"
)

// ExecResult 任务执行结果
type ExecResult struct {
	JobID     string    `json:"job_id"`
	JobName   string    `json:"job_name"`
	Command   string    `json:"command"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Duration  float64   `json:"duration_seconds"`
	ExitCode  int       `json:"exit_code"`
	Output    string    `json:"output"`
	Error     string    `json:"error"`
	Success   bool      `json:"success"`
	Retry     int       `json:"retry"`
}

// LogEntry 执行日志条目
type LogEntry struct {
	ID       string      `json:"id"`
	JobID    string      `json:"job_id"`
	JobName  string      `json:"job_name"`
	Time     time.Time   `json:"time"`
	Success  bool        `json:"success"`
	Duration float64     `json:"duration"`
	ExitCode int         `json:"exit_code"`
	Output   string      `json:"output"`
	Error    string      `json:"error"`
	Result   *ExecResult `json:"result,omitempty"`
}

// Executor 任务执行器
type Executor struct {
	logDir      string
	logEntries  []LogEntry
	mu          sync.RWMutex
	maxLogCount int

	stopCh    chan struct{}
	closeOnce sync.Once
}

// NewExecutor 创建执行器。
// logDir 为相对路径时以 exe 所在目录为基准，而不是进程 cwd：
// 服务由 SCM 启动时 cwd 是 System32，否则日志会写到系统目录，
// 且 CLI 读的 logs/ 与服务写的 logs/ 不是同一份。
func NewExecutor(logDir string) *Executor {
	if logDir == "" {
		logDir = "logs"
	}
	return &Executor{
		logDir:      paths.Resolve(logDir),
		maxLogCount: 500,
		stopCh:      make(chan struct{}),
	}
}

// LogDir 返回实际使用的日志目录
func (e *Executor) LogDir() string {
	return e.logDir
}

// Close 停止日志清理等后台协程，服务退出时调用
func (e *Executor) Close() {
	e.closeOnce.Do(func() { close(e.stopCh) })
}

// Execute 执行一个任务（含重试逻辑）
func (e *Executor) Execute(job *config.JobConfig) *ExecResult {
	var lastResult *ExecResult

	maxRetry := job.MaxRetry
	if maxRetry < 0 {
		maxRetry = 0
	}

	for retry := 0; retry <= maxRetry; retry++ {
		result := e.executeOnce(job, retry)
		lastResult = result
		if result.Success {
			break
		}
		if retry < maxRetry {
			time.Sleep(time.Duration(retry+1) * 2 * time.Second)
		}
	}

	e.addLog(lastResult)
	return lastResult
}

// executeOnce 执行一次任务
func (e *Executor) executeOnce(job *config.JobConfig, retry int) *ExecResult {
	result := &ExecResult{
		JobID:     job.ID,
		JobName:   job.Name,
		Command:   job.Command,
		StartTime: time.Now(),
		Retry:     retry,
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if job.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(job.Timeout)*time.Second)
		defer cancel()
	}

	cmd := e.buildCommandWithContext(ctx, job)

	if job.WorkDir != "" {
		cmd.Dir = paths.Resolve(job.WorkDir)
	}

	// 设置环境变量（在系统环境变量基础上追加任务自定义变量）
	if len(job.Env) > 0 {
		cmd.Env = append([]string(nil), os.Environ()...)
		for k, v := range job.Env {
			cmd.Env = removeEnvVar(cmd.Env, k+"=")
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime).Seconds()
	// 中文 Windows 的 cmd 输出是 GBK，统一转成 UTF-8 后再存日志 / 推 Web / 发通知
	result.Output = decodeOutput(stdout.Bytes())
	result.Error = decodeOutput(stderr.Bytes())

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.Error = fmt.Sprintf("%s\n超时终止: 任务执行超过 %d 秒，已终止整个子进程树",
				result.Error, job.Timeout)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Error = fmt.Sprintf("%s\n执行错误: %v", result.Error, err)
		}
		result.Success = false
	} else {
		result.ExitCode = 0
		result.Success = true
	}

	return result
}

// removeEnvVar 从环境变量列表中移除指定前缀的变量
func removeEnvVar(envs []string, prefix string) []string {
	result := make([]string, 0, len(envs))
	for _, e := range envs {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}

func (e *Executor) buildCommand(job *config.JobConfig) *exec.Cmd {
	return e.buildCommandWithContext(context.Background(), job)
}

// buildCommandWithContext 构建命令。
//
// 关键设计：
//  1. Windows 上用 cmd /c + CREATE_NO_WINDOW + HideWindow，
//     子进程继承服务的 Session 0，不弹窗口、不需要用户登录。
//  2. 用 Cmd.Cancel 接管超时行为：默认 CommandContext 只会 Kill cmd.exe，
//     bat 里再拉起的孙子进程会变成孤儿继续跑。这里改成杀整个进程树。
//  3. WaitDelay 兜底，避免子进程持有 stdout 管道句柄导致 Wait 永久阻塞。
func (e *Executor) buildCommandWithContext(ctx context.Context, job *config.JobConfig) *exec.Cmd {
	cmdStr := job.Command

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", cmdStr)
	}

	setSysProcAttr(cmd)
	cmd.Cancel = func() error {
		return killProcessTree(cmd)
	}
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

// addLog 添加执行日志
func (e *Executor) addLog(result *ExecResult) {
	if result == nil {
		return
	}
	entry := LogEntry{
		ID:       fmt.Sprintf("%s-%d", result.JobID, result.StartTime.UnixNano()),
		JobID:    result.JobID,
		JobName:  result.JobName,
		Time:     result.StartTime,
		Success:  result.Success,
		Duration: result.Duration,
		ExitCode: result.ExitCode,
		Output:   result.Output,
		Error:    result.Error,
		Result:   result,
	}

	e.mu.Lock()
	e.logEntries = append(e.logEntries, entry)
	if len(e.logEntries) > e.maxLogCount {
		e.logEntries = e.logEntries[len(e.logEntries)-e.maxLogCount:]
	}
	e.mu.Unlock()

	e.writeLogFile(&entry)
}

// safeLogName 把任务 ID 收敛成单个文件名。
// 正常写入路径已由 config.validateJobID 挡住非法 ID，但用户可以直接编辑
// crontab.json 绕过；这里做第二层防御，确保日志永远不会写到日志目录之外。
func safeLogName(id string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' || r < 0x20:
			return '_'
		default:
			return r
		}
	}, id)
	clean = strings.Trim(clean, ". ")
	if clean == "" {
		clean = "_invalid_id"
	}
	return clean
}

// writeLogFile 写入文件日志
func (e *Executor) writeLogFile(entry *LogEntry) {
	logFile := filepath.Join(e.logDir, safeLogName(entry.JobID)+".log")

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("[%s] 任务: %s | 状态: ", entry.Time.Format("2006-01-02 15:04:05"), entry.JobName))
	if entry.Success {
		buf.WriteString("成功")
	} else {
		buf.WriteString("失败")
	}
	buf.WriteString(fmt.Sprintf(" | 耗时: %.2fs | 退出码: %d\n", entry.Duration, entry.ExitCode))
	if entry.Output != "" {
		buf.WriteString("--- 输出 ---\n")
		buf.WriteString(entry.Output)
		if !strings.HasSuffix(entry.Output, "\n") {
			buf.WriteString("\n")
		}
	}
	if entry.Error != "" {
		buf.WriteString("--- 错误 ---\n")
		buf.WriteString(entry.Error)
		if !strings.HasSuffix(entry.Error, "\n") {
			buf.WriteString("\n")
		}
	}
	buf.WriteString(strings.Repeat("=", 60))
	buf.WriteString("\n\n")

	f, err := openAppend(logFile)
	if err != nil {
		fmt.Printf("[WinCron] 写日志失败 (%s): %v\n", logFile, err)
		return
	}
	defer f.Close()
	_, _ = io.Copy(f, &buf)
}

// CleanOldLogs 删除超过保留天数的任务日志。
// environment.log 每次服务启动都会覆盖，不参与清理。
func (e *Executor) CleanOldLogs(retentionDays int) int {
	if retentionDays <= 0 {
		return 0
	}
	entries, err := os.ReadDir(e.logDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	removed := 0
	for _, en := range entries {
		if en.IsDir() || !strings.HasSuffix(en.Name(), ".log") || en.Name() == "environment.log" {
			continue
		}
		fi, err := en.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(e.logDir, en.Name())); err == nil {
			removed++
		}
	}
	if removed > 0 {
		fmt.Printf("[WinCron] 已清理 %d 个超过 %d 天的日志文件\n", removed, retentionDays)
	}
	return removed
}

// StartLogCleanup 启动时清理一次，之后每 24 小时清理一次
func (e *Executor) StartLogCleanup(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	go func() {
		e.CleanOldLogs(retentionDays)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.CleanOldLogs(retentionDays)
			}
		}
	}()
}

// GetLogs 获取日志列表
func (e *Executor) GetLogs(jobID string, limit int) []LogEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]LogEntry, 0)
	for i := len(e.logEntries) - 1; i >= 0; i-- {
		if jobID == "" || e.logEntries[i].JobID == jobID {
			result = append(result, e.logEntries[i])
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result
}

// GetAllLogs 获取所有日志
func (e *Executor) GetAllLogs(limit int) []LogEntry {
	return e.GetLogs("", limit)
}
