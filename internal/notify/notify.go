package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"wincron/internal/config"
	"wincron/internal/executor"
)

// Notifier 通知器
type Notifier struct {
	smtpHost string
	smtpPort string
	smtpUser string
	smtpPass string
}

// NewNotifier 创建通知器
func NewNotifier() *Notifier {
	return &Notifier{}
}

// SetSMTPConfig 设置SMTP配置
func (n *Notifier) SetSMTPConfig(host, port, user, pass string) {
	n.smtpHost = host
	n.smtpPort = port
	n.smtpUser = user
	n.smtpPass = pass
}

// Notify 根据任务配置发送通知
func (n *Notifier) Notify(job *config.JobConfig, result *executor.ExecResult) {
	notifyCfg := job.Notify

	// 判断是否需要通知
	shouldNotify := false
	if result.Success && notifyCfg.OnSuccess {
		shouldNotify = true
	} else if !result.Success && notifyCfg.OnFailure {
		shouldNotify = true
	}

	if !shouldNotify {
		return
	}

	// 构建通知消息
	title, body := n.buildMessage(job, result)

	// 发送Webhook通知
	for _, webhookURL := range notifyCfg.Webhooks {
		go n.sendWebhook(webhookURL, job, result, title, body)
	}

	// 发送邮件通知
	if len(notifyCfg.Emails) > 0 && n.smtpHost != "" {
		go n.sendEmail(notifyCfg.Emails, title, body)
	}
}

// buildMessage 构建通知消息
func (n *Notifier) buildMessage(job *config.JobConfig, result *executor.ExecResult) (string, string) {
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
		output := result.Output
		if len(output) > 2000 {
			output = output[:2000] + "\n... (输出已截断)"
		}
		body.WriteString(fmt.Sprintf("\n--- 标准输出 ---\n%s\n", output))
	}

	if result.Error != "" {
		errOut := result.Error
		if len(errOut) > 2000 {
			errOut = errOut[:2000] + "\n... (错误输出已截断)"
		}
		body.WriteString(fmt.Sprintf("\n--- 错误输出 ---\n%s\n", errOut))
	}

	return title, body.String()
}

// sendWebhook 发送Webhook通知
func (n *Notifier) sendWebhook(url string, job *config.JobConfig, result *executor.ExecResult, title, body string) {
	payload := map[string]interface{}{
		"title":      title,
		"body":       body,
		"job_id":     job.ID,
		"job_name":   job.Name,
		"command":    job.Command,
		"success":    result.Success,
		"exit_code":  result.ExitCode,
		"duration":   result.Duration,
		"start_time": result.StartTime,
		"end_time":   result.EndTime,
		"output":     result.Output,
		"error":      result.Error,
		"timestamp":  time.Now(),
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("[WinCron] Webhook JSON编码失败: %v\n", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("[WinCron] Webhook发送失败 (%s): %v\n", url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Printf("[WinCron] Webhook返回错误状态码: %d (%s)\n", resp.StatusCode, url)
	}
}

// sendEmail 发送邮件通知
func (n *Notifier) sendEmail(to []string, subject, body string) {
	if n.smtpHost == "" || n.smtpPort == "" {
		return
	}

	addr := fmt.Sprintf("%s:%s", n.smtpHost, n.smtpPort)
	auth := smtp.PlainAuth("", n.smtpUser, n.smtpPass, n.smtpHost)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		n.smtpUser, strings.Join(to, ","), subject, body)

	err := smtp.SendMail(addr, auth, n.smtpUser, to, []byte(msg))
	if err != nil {
		fmt.Printf("[WinCron] 邮件发送失败: %v\n", err)
	}
}
