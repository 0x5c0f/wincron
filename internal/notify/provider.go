package notify

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	nnotify "github.com/nikoksr/notify"
	"github.com/nikoksr/notify/service/bark"
	"github.com/nikoksr/notify/service/dingding"
	"github.com/nikoksr/notify/service/discord"
	nhttp "github.com/nikoksr/notify/service/http"
	"github.com/nikoksr/notify/service/lark"
	"github.com/nikoksr/notify/service/mail"
	"github.com/nikoksr/notify/service/msteams"
	"github.com/nikoksr/notify/service/pushover"
	"github.com/nikoksr/notify/service/slack"
	"github.com/nikoksr/notify/service/telegram"

	"wincron/internal/config"
)

// buildService 把一个命名渠道转换成 notify 的 Notifier。
//
// 所有渠道都由 nikoksr/notify 的 service 子包实现；企业微信复用
// service/http 的自定义 BuildPayload，仍然完全在 notify 框架内运行。
// 项目内不再保留任何自研的 webhook / SMTP 发送代码。
func buildService(ch config.NotifyChannel) (nnotify.Notifier, error) {
	c, err := expandChannel(ch)
	if err != nil {
		return nil, err
	}

	switch c.Type {
	case "http":
		svc := nhttp.New()
		urls := c.URLs
		if len(urls) == 0 && c.URL != "" {
			urls = []string{c.URL}
		}
		if len(urls) == 0 {
			return nil, fmt.Errorf("渠道 %q: http 需要至少一个 urls", c.Name)
		}
		svc.AddReceiversURLs(urls...)
		return svc, nil

	case "wecom":
		if c.URL == "" {
			return nil, fmt.Errorf("渠道 %q: wecom 需要 url", c.Name)
		}
		svc := nhttp.New()
		svc.AddReceivers(&nhttp.Webhook{
			URL:         c.URL,
			ContentType: "application/json; charset=utf-8",
			Method:      "POST",
			// newRequest 会执行 req.Header = hook.Header 再 Set("User-Agent")。
			// 手工构造 Webhook 时必须初始化这个 map，否则会在发送时
			// panic: assignment to entry in nil map（AddReceiversURLs 内部
			// 的 newWebhook 已经初始化过，所以只有自定义 Webhook 有这个坑）。
			Header:       http.Header{},
			BuildPayload: wecomPayload(c),
		})
		return svc, nil

	case "dingding":
		if c.Token == "" || c.Secret == "" {
			return nil, fmt.Errorf("渠道 %q: dingding 需要 token 与 secret", c.Name)
		}
		return dingding.New(&dingding.Config{Token: c.Token, Secret: c.Secret}), nil

	case "lark":
		if c.URL == "" {
			return nil, fmt.Errorf("渠道 %q: lark 需要 url", c.Name)
		}
		return lark.NewWebhookService(c.URL), nil

	case "slack":
		if c.Token == "" || len(c.Receivers) == 0 {
			return nil, fmt.Errorf("渠道 %q: slack 需要 token 与 receivers", c.Name)
		}
		svc := slack.New(c.Token)
		svc.AddReceivers(c.Receivers...)
		return svc, nil

	case "telegram":
		if c.Token == "" || len(c.ChatIDs) == 0 {
			return nil, fmt.Errorf("渠道 %q: telegram 需要 token 与 chat_ids", c.Name)
		}
		svc, err := telegramNewWithTimeout(c.Token)
		if err != nil {
			return nil, fmt.Errorf("渠道 %q: 初始化 telegram 失败: %w", c.Name, err)
		}
		svc.AddReceivers(c.ChatIDs...)
		return svc, nil

	case "bark":
		if c.Token == "" {
			return nil, fmt.Errorf("渠道 %q: bark 需要 token(device_key)", c.Name)
		}
		return bark.NewWithServers(c.Token, c.Servers...), nil

	case "mail":
		if c.SMTPHost == "" || c.From == "" || len(c.Receivers) == 0 {
			return nil, fmt.Errorf("渠道 %q: mail 需要 smtp_host、from 与 receivers", c.Name)
		}
		svc := mail.New(c.From, c.SMTPHost)
		if c.Username != "" || c.Password != "" {
			host, _, err := net.SplitHostPort(c.SMTPHost)
			if err != nil {
				host = c.SMTPHost
			}
			svc.AuthenticateSMTP("", c.Username, c.Password, host)
		}
		svc.AddReceivers(c.Receivers...)
		svc.BodyFormat(mail.PlainText)
		return svc, nil

	case "msteams":
		if len(c.Receivers) == 0 {
			return nil, fmt.Errorf("渠道 %q: msteams 需要 receivers(webhook URL)", c.Name)
		}
		svc := msteams.New()
		svc.AddReceivers(c.Receivers...)
		return svc, nil

	case "discord":
		if c.Token == "" || len(c.Receivers) == 0 {
			return nil, fmt.Errorf("渠道 %q: discord 需要 token 与 receivers", c.Name)
		}
		svc := discord.New()
		if err := svc.AuthenticateWithBotToken(c.Token); err != nil {
			return nil, fmt.Errorf("渠道 %q: discord 鉴权失败: %w", c.Name, err)
		}
		svc.AddReceivers(c.Receivers...)
		return svc, nil

	case "pushover":
		if c.Token == "" || len(c.Receivers) == 0 {
			return nil, fmt.Errorf("渠道 %q: pushover 需要 token 与 receivers", c.Name)
		}
		svc := pushover.New(c.Token)
		svc.AddReceivers(c.Receivers...)
		return svc, nil

	default:
		return nil, fmt.Errorf("渠道 %q: 不支持的 type %q（支持: %s）",
			c.Name, c.Type, strings.Join(config.SupportedChannelTypes, ", "))
	}
}

// telegramNewWithTimeout 给 notify 的 telegram.New 套一层超时。
//
// telegram.New 内部会调用 go-telegram-bot-api 的 NewBotAPI -> GetMe，
// 而后者用的是 &http.Client{}（没有超时）。服务运行在 Session 0 且网络不通时，
// 这个请求可能长时间挂住，把服务启动或配置热加载整个卡死。
// 超时后该渠道标记为不可用，服务其余部分照常运行。
const telegramInitTimeout = 5 * time.Second

func telegramNewWithTimeout(token string) (*telegram.Telegram, error) {
	type result struct {
		svc *telegram.Telegram
		err error
	}
	ch := make(chan result, 1)
	go func() {
		svc, err := telegram.New(token)
		ch <- result{svc: svc, err: err}
	}()
	select {
	case r := <-ch:
		return r.svc, r.err
	case <-time.After(telegramInitTimeout):
		return nil, fmt.Errorf("连接 Telegram API 超过 %s（网络不可达或 token 无效）", telegramInitTimeout)
	}
}

// expandChannel 展开所有字符串字段里的 ${ENV:NAME} 引用。
func expandChannel(ch config.NotifyChannel) (config.NotifyChannel, error) {
	var firstErr error
	ex := func(field, s string) string {
		if firstErr != nil {
			return s
		}
		v, err := config.ExpandSecret(s)
		if err != nil {
			firstErr = fmt.Errorf("渠道 %q 的 %s: %w", ch.Name, field, err)
			return s
		}
		return v
	}
	exList := func(field string, in []string) []string {
		out := make([]string, len(in))
		for i := range in {
			out[i] = ex(field, in[i])
		}
		return out
	}

	out := ch.Clone()
	out.URL = ex("url", ch.URL)
	out.URLs = exList("urls", ch.URLs)
	out.Token = ex("token", ch.Token)
	out.Secret = ex("secret", ch.Secret)
	out.Username = ex("username", ch.Username)
	out.Password = ex("password", ch.Password)
	out.Receivers = exList("receivers", ch.Receivers)
	out.Servers = exList("servers", ch.Servers)
	out.SMTPHost = ex("smtp_host", ch.SMTPHost)
	out.From = ex("from", ch.From)
	out.MentionedList = exList("mentioned_list", ch.MentionedList)
	out.MentionedMobileList = exList("mentioned_mobile_list", ch.MentionedMobileList)
	if firstErr != nil {
		return ch, firstErr
	}
	return out, nil
}

// truncateUTF8 按字节上限截断，且不会切坏 UTF-8 字符。
// 企业微信 text.content 上限 2048 字节，直接按 rune 截断对中文会超长。
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s)[:maxBytes]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b) + "\n...(已截断)"
}

// wecomPayload 生成企业微信群机器人要求的 JSON schema。
// notify 没有专用 wecom service，但 service/http 允许每个 Webhook 自定义
// BuildPayload，因此这里仍然是一个标准的 notify.Notifier。
func wecomPayload(ch config.NotifyChannel) nhttp.BuildPayloadFn {
	return func(subject, message string) any {
		content := subject + "\n" + message
		text := map[string]any{
			"content": truncateUTF8(content, 2048),
		}
		if len(ch.MentionedList) > 0 {
			text["mentioned_list"] = ch.MentionedList
		}
		if len(ch.MentionedMobileList) > 0 {
			text["mentioned_mobile_list"] = ch.MentionedMobileList
		}
		return map[string]any{
			"msgtype": "text",
			"text":    text,
		}
	}
}
