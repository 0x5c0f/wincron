package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// NotifyChannel 描述一个全局命名通知渠道。
//
// 渠道定义集中在 app.notify_channels，任务只通过名称引用，凭据不会散落在
// 每个任务里。所有字符串字段都支持 ${ENV:NAME} 引用环境变量，避免 token、
// 邮箱密码等敏感信息以明文落盘；缺失的环境变量会在构造渠道时报错，而不是
// 静默把消息发到一个错误地址。
type NotifyChannel struct {
	Name string `json:"name"` // 渠道唯一名称，任务通过它引用
	Type string `json:"type"` // 渠道类型，见 SupportedChannelTypes

	// 通用 Webhook（http / wecom / lark / msteams 使用）
	URL  string   `json:"url,omitempty"`
	URLs []string `json:"urls,omitempty"`

	// 凭据（dingding / slack / telegram / discord / pushover / mail 使用）
	Token    string `json:"token,omitempty"`
	Secret   string `json:"secret,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// 收件目标
	Receivers []string `json:"receivers,omitempty"` // slack 频道 / discord 频道 / pushover 用户 / 邮箱 / msteams webhook
	ChatIDs   []int64  `json:"chat_ids,omitempty"`  // telegram chat id
	Servers   []string `json:"servers,omitempty"`   // bark 服务器

	// 邮件（mail 使用）
	SMTPHost string `json:"smtp_host,omitempty"` // host:port
	From     string `json:"from,omitempty"`

	// 企业微信 @ 提醒
	MentionedList       []string `json:"mentioned_list,omitempty"`
	MentionedMobileList []string `json:"mentioned_mobile_list,omitempty"`

	// 单渠道发送超时（秒），0 表示使用 app.notify_timeout
	Timeout int `json:"timeout,omitempty"`
}

// SupportedChannelTypes 是当前实现支持的渠道类型。
// 除 wecom 之外都直接对应 notify 的一个 service 子包；wecom 复用
// service/http 的自定义 BuildPayload 能力，仍然完全运行在 notify 框架内。
var SupportedChannelTypes = []string{
	"http", "wecom", "dingding", "lark", "slack", "telegram",
	"bark", "mail", "msteams", "discord", "pushover",
}

var envRefPattern = regexp.MustCompile(`\$\{ENV:([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandSecret 展开 ${ENV:NAME} 引用。任一变量未设置都返回错误，
// 避免把空字符串当成有效凭据。
func ExpandSecret(s string) (string, error) {
	if !strings.Contains(s, "${ENV:") {
		return s, nil
	}
	var missing []string
	out := envRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := envRefPattern.FindStringSubmatch(match)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return ""
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("环境变量未设置: %s", strings.Join(missing, ", "))
	}
	if extra := envRefPattern.FindString(out); extra != "" {
		return "", fmt.Errorf("无法识别的环境变量引用: %s", extra)
	}
	return out, nil
}

// Clone 深拷贝渠道，避免 Get() 暴露内部 slice。
func (ch NotifyChannel) Clone() NotifyChannel {
	out := ch
	out.URLs = append([]string(nil), ch.URLs...)
	out.Receivers = append([]string(nil), ch.Receivers...)
	out.ChatIDs = append([]int64(nil), ch.ChatIDs...)
	out.Servers = append([]string(nil), ch.Servers...)
	out.MentionedList = append([]string(nil), ch.MentionedList...)
	out.MentionedMobileList = append([]string(nil), ch.MentionedMobileList...)
	return out
}

// cloneChannels 深拷贝渠道列表。
func cloneChannels(in []NotifyChannel) []NotifyChannel {
	if in == nil {
		return nil
	}
	out := make([]NotifyChannel, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func isSupportedChannelType(t string) bool {
	for _, v := range SupportedChannelTypes {
		if v == t {
			return true
		}
	}
	return false
}

// checkURL 只做 URL 语法校验：含 ${ENV:...} 的值先替换成占位符再解析。
//
// 这里刻意不展开环境变量。validate 常在交互式终端执行，而服务可能跑在
// Session 0 或由 SCM 注入了不同的系统环境变量；在校验阶段要求变量存在
// 会产生假阳性。真正的展开与缺失报错发生在 buildService 构造渠道时，
// 失败渠道会被标记不可用并写进服务日志。
func checkURL(raw, field string) error {
	if raw == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	probe := envRefPattern.ReplaceAllString(raw, "placeholder")
	u, err := url.Parse(probe)
	if err != nil {
		return fmt.Errorf("%s 不是合法 URL: %w", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s 只允许 http/https，当前为 %q", field, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s 缺少主机名", field)
	}
	return nil
}

// ChannelRequiredFields 返回各渠道类型的必填字段，供 Web 表单和文档复用。
func ChannelRequiredFields(t string) []string {
	switch t {
	case "http":
		return []string{"urls"}
	case "wecom", "lark":
		return []string{"url"}
	case "dingding":
		return []string{"token", "secret"}
	case "slack", "discord":
		return []string{"token", "receivers"}
	case "telegram":
		return []string{"token", "chat_ids"}
	case "bark":
		return []string{"token"}
	case "mail":
		return []string{"smtp_host", "from", "receivers"}
	case "msteams":
		return []string{"receivers"}
	case "pushover":
		return []string{"token", "receivers"}
	default:
		return nil
	}
}

// ValidateChannels 返回渠道配置中发现的全部问题，只用于提示，不阻塞加载。
// 构造失败的具体渠道会在通知时被跳过并记录日志。
func ValidateChannels(channels []NotifyChannel) []string {
	var problems []string
	seen := make(map[string]bool, len(channels))
	for i, ch := range channels {
		label := fmt.Sprintf("通知渠道[%d] %q", i+1, ch.Name)
		if strings.TrimSpace(ch.Name) == "" {
			problems = append(problems, fmt.Sprintf("通知渠道[%d] 缺少 name", i+1))
			continue
		}
		// 渠道名会进入 Web 的 data-* 属性和 CLI 参数，禁止引号与控制字符，
		// 避免拼接 HTML/命令行时被注入。
		if ch.Name != strings.TrimSpace(ch.Name) || strings.ContainsAny(ch.Name, " /\\:*?\"<>|'`&;") {
			problems = append(problems, label+" 的名称含非法字符或首尾空白")
		}
		if seen[ch.Name] {
			problems = append(problems, label+" 名称重复，任务引用会产生歧义")
		}
		seen[ch.Name] = true
		if !isSupportedChannelType(ch.Type) {
			problems = append(problems, fmt.Sprintf("%s 使用了不支持的 type %q（支持: %s）",
				label, ch.Type, strings.Join(SupportedChannelTypes, ", ")))
			continue
		}
		switch ch.Type {
		case "http":
			if len(ch.URLs) == 0 && ch.URL == "" {
				problems = append(problems, label+" 缺少 urls")
			}
			for _, u := range ch.URLs {
				if err := checkURL(u, label+".urls"); err != nil {
					problems = append(problems, err.Error())
				}
			}
		case "wecom", "lark":
			if err := checkURL(ch.URL, label+".url"); err != nil {
				problems = append(problems, err.Error())
			}
		case "dingding":
			if ch.Token == "" || ch.Secret == "" {
				problems = append(problems, label+" 缺少 token 或 secret")
			}
		case "slack", "discord", "pushover":
			if ch.Token == "" || len(ch.Receivers) == 0 {
				problems = append(problems, label+" 缺少 token 或 receivers")
			}
		case "telegram":
			if ch.Token == "" || len(ch.ChatIDs) == 0 {
				problems = append(problems, label+" 缺少 token 或 chat_ids")
			}
		case "bark":
			if ch.Token == "" {
				problems = append(problems, label+" 缺少 token（Bark device key）")
			}
		case "mail":
			if ch.SMTPHost == "" || ch.From == "" || len(ch.Receivers) == 0 {
				problems = append(problems, label+" 缺少 smtp_host、from 或 receivers")
			}
		case "msteams":
			if len(ch.Receivers) == 0 {
				problems = append(problems, label+" 缺少 receivers（Teams webhook URL）")
			}
			for _, u := range ch.Receivers {
				if err := checkURL(u, label+".receivers"); err != nil {
					problems = append(problems, err.Error())
				}
			}
		}
		if ch.Timeout < 0 {
			problems = append(problems, label+" 的 timeout 不能为负数")
		}
	}
	return problems
}

// ValidateJobChannels 检查任务引用的渠道名是否都存在、是否重复。
func ValidateJobChannels(jobs []JobConfig, channels []NotifyChannel) []string {
	known := make(map[string]bool, len(channels))
	for _, ch := range channels {
		known[ch.Name] = true
	}
	var problems []string
	for _, j := range jobs {
		seen := make(map[string]bool, len(j.Notify.Channels))
		for _, name := range j.Notify.Channels {
			if !known[name] {
				problems = append(problems, fmt.Sprintf(
					"任务 %q 引用了不存在的通知渠道 %q，该渠道的通知会被跳过", j.ID, name))
			}
			if seen[name] {
				problems = append(problems, fmt.Sprintf("任务 %q 重复引用通知渠道 %q", j.ID, name))
			}
			seen[name] = true
		}
	}
	return problems
}

// legacyNotifyFields 只用于读取旧版 crontab.json，不参与序列化。
type legacyNotifyFields struct {
	App struct {
		SMTPHost string `json:"smtp_host"`
		SMTPPort string `json:"smtp_port"`
		SMTPUser string `json:"smtp_user"`
		SMTPPass string `json:"smtp_pass"`
	} `json:"app"`
	Jobs []struct {
		ID     string `json:"id"`
		Notify struct {
			Webhooks []string `json:"webhooks"`
			Emails   []string `json:"emails"`
		} `json:"notify"`
	} `json:"jobs"`
}

// MigrateLegacyNotify 把旧版 webhooks / emails / smtp_* 配置一次性转换成
// notify 风格的命名渠道。旧的自研渠道实现已经删除，这里只负责不让用户
// 现有的 crontab.json 在升级后静默丢通知。
//
// 每个任务的收件人是独立的，因此按任务生成专属渠道，保证语义完全保真。
// 返回 migrated=true 时调用方应把新格式写回文件，之后旧字段彻底消失。
func MigrateLegacyNotify(cfg *CrontabFile, raw []byte) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	var legacy legacyNotifyFields
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return false, fmt.Errorf("读取旧版通知字段失败: %w", err)
	}

	// 已经迁移过：没有任何旧字段。
	hasLegacy := legacy.App.SMTPHost != "" || legacy.App.SMTPUser != "" || legacy.App.SMTPPass != ""
	for _, lj := range legacy.Jobs {
		if len(lj.Notify.Webhooks) > 0 || len(lj.Notify.Emails) > 0 {
			hasLegacy = true
			break
		}
	}
	if !hasLegacy {
		return false, nil
	}

	used := make(map[string]bool, len(cfg.App.NotifyChannels))
	for _, ch := range cfg.App.NotifyChannels {
		used[ch.Name] = true
	}
	// 写回失败时热加载会再次读到旧文件；对已生成的 legacy-* 渠道直接复用，
	// 避免每轮加载都追加一份重复渠道。
	uniqueName := func(base string) (string, bool) {
		if used[base] && strings.HasPrefix(base, "legacy-") {
			return base, false
		}
		name := base
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[name] = true
		return name, true
	}

	smtpAddr := legacy.App.SMTPHost
	if smtpAddr != "" && legacy.App.SMTPPort != "" &&
		!strings.Contains(smtpAddr, ":") {
		smtpAddr = smtpAddr + ":" + legacy.App.SMTPPort
	}

	legacyByID := make(map[string]struct {
		Webhooks []string
		Emails   []string
	}, len(legacy.Jobs))
	for _, lj := range legacy.Jobs {
		legacyByID[lj.ID] = struct {
			Webhooks []string
			Emails   []string
		}{lj.Notify.Webhooks, lj.Notify.Emails}
	}

	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]
		old, ok := legacyByID[job.ID]
		if !ok {
			continue
		}
		if len(old.Webhooks) > 0 {
			name, created := uniqueName("legacy-" + job.ID + "-http")
			if created {
				cfg.App.NotifyChannels = append(cfg.App.NotifyChannels, NotifyChannel{
					Name: name,
					Type: "http",
					URLs: append([]string(nil), old.Webhooks...),
				})
			}
			job.Notify.Channels = append(job.Notify.Channels, name)
		}
		if len(old.Emails) > 0 {
			name, created := uniqueName("legacy-" + job.ID + "-mail")
			if created {
				cfg.App.NotifyChannels = append(cfg.App.NotifyChannels, NotifyChannel{
					Name:      name,
					Type:      "mail",
					SMTPHost:  smtpAddr,
					From:      legacy.App.SMTPUser,
					Username:  legacy.App.SMTPUser,
					Password:  legacy.App.SMTPPass,
					Receivers: append([]string(nil), old.Emails...),
				})
			}
			job.Notify.Channels = append(job.Notify.Channels, name)
		}
	}
	return true, nil
}
