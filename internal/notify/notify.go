package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	nnotify "github.com/nikoksr/notify"

	"wincron/internal/config"
	"wincron/internal/executor"
)

// LogFunc 是通知层写日志的最小接口。Windows 服务运行在 Session 0，
// fmt.Printf 的内容没人看得到，所以服务侧会把 service.Logger 接进来。
type LogFunc func(format string, args ...any)

const defaultNotifyTimeout = 10 * time.Second

type channelEntry struct {
	name    string
	typ     string
	service nnotify.Notifier
	timeout time.Duration
}

// ChannelStatus 供 Web 展示渠道当前是否可用。
type ChannelStatus struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
}

// Notifier 持有按名称索引的 notify service 实例。
// 每个任务按 channels 选择渠道，发送时为每个渠道构造独立的 Notify 实例，
// 从而保留 notify 原生的广播语义，又不会把消息串到别的任务。
type Notifier struct {
	mu             sync.RWMutex
	channels       map[string]channelEntry
	defaultTimeout time.Duration
	buildErrors    []string
	logf           LogFunc
	wg             sync.WaitGroup
	stopped        bool
}

// NewNotifier 构造通知器。单个渠道构造失败不会让服务起不来，
// 失败原因可通过 BuildErrors() 取到并在 validate / Web 上展示。
func NewNotifier(channels []config.NotifyChannel, timeoutSeconds int, logf LogFunc) *Notifier {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	n := &Notifier{
		defaultTimeout: durationFromSeconds(timeoutSeconds),
		logf:           logf,
	}
	n.Reload(channels, timeoutSeconds)
	return n
}

func durationFromSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultNotifyTimeout
	}
	return time.Duration(seconds) * time.Second
}

// Reload 依据最新配置重建渠道，配置热加载时调用。
// 全程先构建新的 map 再一次性替换，避免发送过程中读到半新半旧的状态。
func (n *Notifier) Reload(channels []config.NotifyChannel, timeoutSeconds int) {
	built := make(map[string]channelEntry, len(channels))
	var problems []string
	seen := make(map[string]bool, len(channels))
	baseTimeout := durationFromSeconds(timeoutSeconds)

	for _, ch := range channels {
		if seen[ch.Name] {
			problems = append(problems, fmt.Sprintf("通知渠道 %q 名称重复，后者已忽略", ch.Name))
			continue
		}
		seen[ch.Name] = true
		svc, err := buildService(ch)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		timeout := baseTimeout
		if ch.Timeout > 0 {
			timeout = time.Duration(ch.Timeout) * time.Second
		}
		built[ch.Name] = channelEntry{name: ch.Name, typ: ch.Type, service: svc, timeout: timeout}
	}

	n.mu.Lock()
	n.channels = built
	n.defaultTimeout = baseTimeout
	n.buildErrors = problems
	n.mu.Unlock()

	for _, p := range problems {
		n.log("[WinCron] 通知渠道不可用: %s\n", p)
	}
}

// SetLogger 在 Windows 服务拿到 service.Logger 之后替换默认的标准输出日志。
func (n *Notifier) SetLogger(logf LogFunc) {
	if logf == nil {
		return
	}
	n.mu.Lock()
	n.logf = logf
	n.mu.Unlock()
}

// log 在持锁状态下取日志函数，避免 SetLogger 与在途通知产生数据竞争。
func (n *Notifier) log(format string, args ...any) {
	n.mu.RLock()
	f := n.logf
	n.mu.RUnlock()
	f(format, args...)
}

// BuildErrors 返回构造失败的渠道说明。
func (n *Notifier) BuildErrors() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]string(nil), n.buildErrors...)
}

// Statuses 返回全部已配置渠道的就绪状态，供 Web 页面与 validate 使用。
func (n *Notifier) Statuses() []ChannelStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]ChannelStatus, 0, len(n.channels)+len(n.buildErrors))
	for name, e := range n.channels {
		out = append(out, ChannelStatus{Name: name, Type: e.typ, Ready: true})
	}
	for _, p := range n.buildErrors {
		out = append(out, ChannelStatus{Ready: false, Problem: p})
	}
	return out
}

// Notify 在任务结束后异步发送通知，不阻塞调度线程。
// 每次调用都会等待自身派发的全部渠道（各自带独立超时），
// 错误统一写进服务日志，避免 Session 0 里静默丢通知。
func (n *Notifier) Notify(job *config.JobConfig, result *executor.ExecResult) {
	if job == nil || result == nil {
		return
	}
	if !shouldNotify(job, result) || len(job.Notify.Channels) == 0 {
		return
	}
	// 检查 stopped 与 wg.Add 必须在同一把写锁内完成。
	// 否则 Wait() 可能先设置 stopped 再进入 wg.Wait()，而这里正好在
	// 两次调用之间执行 wg.Add(1) —— 会触发 WaitGroup 的并发误用，
	// 也可能让服务退出时漏掉最后一条告警。
	n.mu.Lock()
	if n.stopped {
		n.mu.Unlock()
		return
	}
	n.wg.Add(1)
	n.mu.Unlock()
	go func() {
		defer n.wg.Done()
		for _, err := range n.NotifySync(job, result) {
			n.log("[WinCron] 通知发送失败: %v\n", err)
		}
	}()
}

func shouldNotify(job *config.JobConfig, result *executor.ExecResult) bool {
	if result.Success {
		return job.Notify.OnSuccess
	}
	return job.Notify.OnFailure
}

// NotifySync 同步发送并返回每个渠道的错误，供单元测试和 Web「测试发送」使用。
func (n *Notifier) NotifySync(job *config.JobConfig, result *executor.ExecResult) []error {
	if job == nil || result == nil || !shouldNotify(job, result) {
		return nil
	}
	title, body := buildMessage(job, result)

	n.mu.RLock()
	snapshot := make(map[string]channelEntry, len(n.channels))
	for k, v := range n.channels {
		snapshot[k] = v
	}
	n.mu.RUnlock()

	var errs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range job.Notify.Channels {
		entry, ok := snapshot[name]
		if !ok {
			mu.Lock()
			errs = append(errs, fmt.Errorf("任务 %q 引用了不存在的通知渠道 %q", job.ID, name))
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(e channelEntry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
			defer cancel()

			// 每个渠道用独立实例，避免 receivers 在任务之间互相串。
			sender := nnotify.NewWithServices(e.service)
			if err := sender.Send(ctx, title, body); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("渠道 %q(%s): %w", e.name, e.typ, err))
				mu.Unlock()
			}
		}(entry)
	}
	wg.Wait()
	return errs
}

// TestChannel 立即向指定渠道发送一条测试消息。
func (n *Notifier) TestChannel(name string) error {
	n.mu.RLock()
	entry, ok := n.channels[name]
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("通知渠道 %q 不存在或不可用", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), entry.timeout)
	defer cancel()
	sender := nnotify.NewWithServices(entry.service)
	subject := "[WinCron] 通知渠道测试"
	body := fmt.Sprintf("渠道: %s\n类型: %s\n时间: %s\n这是一条来自 WinCron 的测试通知。",
		name, entry.typ, time.Now().Format("2006-01-02 15:04:05"))
	if err := sender.Send(ctx, subject, body); err != nil {
		return fmt.Errorf("渠道 %q(%s): %w", name, entry.typ, err)
	}
	return nil
}

// Wait 等待所有在途通知发送完毕，服务停止时调用。
func (n *Notifier) Wait() {
	n.mu.Lock()
	n.stopped = true
	n.mu.Unlock()
	n.wg.Wait()
}

// buildMessage 构造通知标题与正文。输出按字节截断，避免某个任务把
// 几 MB 日志塞进企业微信 / Telegram 导致整个渠道失败。
func buildMessage(job *config.JobConfig, result *executor.ExecResult) (string, string) {
	status := "成功"
	if !result.Success {
		status = "失败"
	}
	title := fmt.Sprintf("[WinCron] 任务%s: %s", status, job.Name)

	var body strings.Builder
	body.WriteString(fmt.Sprintf("任务名称: %s\n", job.Name))
	body.WriteString(fmt.Sprintf("任务ID: %s\n", job.ID))
	body.WriteString(fmt.Sprintf("执行状态: %s\n", status))
	body.WriteString(fmt.Sprintf("执行命令: %s\n", job.Command))
	body.WriteString(fmt.Sprintf("开始时间: %s\n", result.StartTime.Format("2006-01-02 15:04:05")))
	body.WriteString(fmt.Sprintf("结束时间: %s\n", result.EndTime.Format("2006-01-02 15:04:05")))
	body.WriteString(fmt.Sprintf("耗时: %.2f 秒\n", result.Duration))
	body.WriteString(fmt.Sprintf("退出码: %d\n", result.ExitCode))
	if result.Retry > 0 {
		body.WriteString(fmt.Sprintf("重试次数: %d\n", result.Retry))
	}
	if result.Output != "" {
		body.WriteString(fmt.Sprintf("\n--- 标准输出 ---\n%s\n", truncateUTF8(result.Output, 2000)))
	}
	if result.Error != "" {
		body.WriteString(fmt.Sprintf("\n--- 错误输出 ---\n%s\n", truncateUTF8(result.Error, 2000)))
	}
	return title, body.String()
}
