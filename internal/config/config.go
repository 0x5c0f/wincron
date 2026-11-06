package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"wincron/internal/cronexpr"
)

// JobConfig 定义一个定时任务
type JobConfig struct {
	ID       string            `json:"id"`        // 任务唯一ID
	Name     string            `json:"name"`      // 任务名称
	Cron     string            `json:"cron"`      // cron表达式（5字段或6字段，自动兼容）
	Command  string            `json:"command"`   // 要执行的命令（可含空格、重定向、管道）
	WorkDir  string            `json:"work_dir"`  // 工作目录（可选）
	Enabled  bool              `json:"enabled"`   // 是否启用
	Timeout  int               `json:"timeout"`   // 超时时间（秒），0表示不超时
	MaxRetry int               `json:"max_retry"` // 最大重试次数
	Notify   NotifyConfig      `json:"notify"`    // 通知配置
	Env      map[string]string `json:"env"`       // 环境变量（可选）
}

// NotifyConfig 通知配置
type NotifyConfig struct {
	OnSuccess bool     `json:"on_success"` // 成功时通知
	OnFailure bool     `json:"on_failure"` // 失败时通知
	Webhooks  []string `json:"webhooks"`   // webhook URL列表
	Emails    []string `json:"emails"`     // 邮件收件人列表
}

// AppConfig 应用配置
type AppConfig struct {
	ServiceName      string `json:"service_name"`       // Windows服务名称
	DisplayName      string `json:"display_name"`       // 服务显示名称
	Description      string `json:"description"`        // 服务描述
	ServiceUser      string `json:"service_user"`       // 服务运行账户（留空=LocalSystem）
	ServicePassword  string `json:"service_password"`   // 服务运行账户密码
	WebEnabled       bool   `json:"web_enabled"`        // 是否启用Web管理
	WebPort          int    `json:"web_port"`           // Web服务端口
	WebUsername      string `json:"web_username"`       // Web登录用户名
	WebPassword      string `json:"web_password"`       // Web登录密码
	LogDir           string `json:"log_dir"`            // 日志目录
	LogRetentionDays int    `json:"log_retention_days"` // 日志保留天数
	EnableSeconds    bool   `json:"enable_seconds"`     // 兼容保留：5/6字段现已自动识别，此开关不再影响解析
	SMTPHost         string `json:"smtp_host"`          // SMTP服务器（留空则不发邮件）
	SMTPPort         string `json:"smtp_port"`          // SMTP端口
	SMTPUser         string `json:"smtp_user"`          // SMTP账号，同时作为发件人
	SMTPPass         string `json:"smtp_pass"`          // SMTP密码/授权码
}

// CrontabFile crontab配置文件结构
type CrontabFile struct {
	App  AppConfig   `json:"app"`
	Jobs []JobConfig `json:"jobs"`
}

// ConfigManager 配置管理器，支持热加载
type ConfigManager struct {
	filePath string
	config   *CrontabFile
	mu       sync.RWMutex
	modTime  time.Time
	size     int64
	onReload func(*CrontabFile)

	// loadIssues 记录 Load() 时自动纠正或发现的应用级配置问题。
	// 用户直接用记事本改 crontab.json 时，这类问题不该被静默吞掉：
	// validate / list / Web 的 config_issues 都会把它显示出来。
	loadIssues []string
}

// decodeConfigBytes 还原用户用 Windows 记事本编辑后可能产生的编码差异。
//  1. UTF-8 BOM：记事本「另存为 UTF-8」默认带 BOM，encoding/json 不认；
//  2. ANSI/GBK：中文系统记事本默认 ANSI，直接按 UTF-8 解会得到乱码或解析失败。
func decodeConfigBytes(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return data, nil
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err == nil && utf8.Valid(decoded) {
		return decoded, nil
	}
	return nil, fmt.Errorf("配置文件既不是 UTF-8 也不是 GBK/GB18030 编码，请用记事本另存为 UTF-8")
}

// DefaultConfig 返回默认配置
func DefaultConfig() *CrontabFile {
	return &CrontabFile{
		App: AppConfig{
			ServiceName:      "WinCron",
			DisplayName:      "WinCron 定时任务服务",
			Description:      "Windows Cron 定时任务管理服务，替代系统计划任务",
			WebEnabled:       true,
			WebPort:          9090,
			WebUsername:      "admin",
			WebPassword:      "CHANGE_ME_BEFORE_USE",
			LogDir:           "logs",
			LogRetentionDays: 30,
			EnableSeconds:    true,
			SMTPPort:         "587",
		},
		Jobs: []JobConfig{},
	}
}

// NewConfigManager 创建配置管理器
func NewConfigManager(filePath string) *ConfigManager {
	return &ConfigManager{
		filePath: filePath,
		config:   DefaultConfig(),
	}
}

// cloneJob 深拷贝单个任务，避免 Get/GetJobs 暴露内部 slice 和 map
func cloneJob(j JobConfig) JobConfig {
	out := j
	out.Notify.Webhooks = append([]string(nil), j.Notify.Webhooks...)
	out.Notify.Emails = append([]string(nil), j.Notify.Emails...)
	if j.Env != nil {
		out.Env = make(map[string]string, len(j.Env))
		for k, v := range j.Env {
			out.Env[k] = v
		}
	}
	return out
}

// cloneConfig 深拷贝整个配置
func cloneConfig(c *CrontabFile) *CrontabFile {
	if c == nil {
		return nil
	}
	out := *c
	out.Jobs = make([]JobConfig, len(c.Jobs))
	for i, j := range c.Jobs {
		out.Jobs[i] = cloneJob(j)
	}
	return &out
}

// Load 加载配置文件
func (cm *ConfigManager) Load() error {
	data, err := os.ReadFile(cm.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			cm.mu.Lock()
			defer cm.mu.Unlock()
			return cm.saveLocked()
		}
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	data, err = decodeConfigBytes(data)
	if err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}

	var cfg CrontabFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 合并默认值（只在字段为零值时补，不覆盖用户显式配置）
	def := DefaultConfig().App
	var issues []string
	if cfg.App.ServiceName == "" {
		cfg.App.ServiceName = def.ServiceName
	}
	if cfg.App.DisplayName == "" {
		cfg.App.DisplayName = def.DisplayName
	}
	if cfg.App.Description == "" {
		cfg.App.Description = def.Description
	}
	if cfg.App.WebPort == 0 {
		cfg.App.WebPort = def.WebPort
	} else if cfg.App.WebPort < 1 || cfg.App.WebPort > 65535 {
		issues = append(issues, fmt.Sprintf(
			"Web端口 %d 无效（有效范围 1-65535），已回退到默认端口 %d", cfg.App.WebPort, def.WebPort))
		cfg.App.WebPort = def.WebPort
	}
	if cfg.App.LogDir == "" {
		cfg.App.LogDir = def.LogDir
	}
	if cfg.App.LogRetentionDays == 0 {
		cfg.App.LogRetentionDays = def.LogRetentionDays
	}
	if cfg.App.SMTPPort == "" {
		cfg.App.SMTPPort = def.SMTPPort
	}

	// 只填了用户名或只填了密码是危险的半配置：
	// 要么出现「空密码也能登录」，要么「登录成功但会话立刻失效」。
	// 这里不静默关闭认证（那会把管理界面暴露出去），而是记录问题并由
	// Web 层 fail-closed 地拒绝登录，同时在所有校验入口明确提示用户。
	if (cfg.App.WebUsername == "") != (cfg.App.WebPassword == "") {
		missing := "web_password"
		if cfg.App.WebUsername == "" {
			missing = "web_username"
		}
		issues = append(issues, "Web 认证配置不完整：缺少 "+missing+
			"，web_username 与 web_password 必须同时填写；当前已按「拒绝登录」处理，请补全配置")
	}

	if cfg.Jobs == nil {
		cfg.Jobs = []JobConfig{}
	}

	cm.mu.Lock()
	cm.config = &cfg
	cm.loadIssues = issues
	if fi, err := os.Stat(cm.filePath); err == nil {
		cm.modTime = fi.ModTime()
		cm.size = fi.Size()
	}
	cm.mu.Unlock()

	return nil
}

// Get 获取当前配置的深拷贝（线程安全，调用方可自由读写）
func (cm *ConfigManager) Get() *CrontabFile {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cloneConfig(cm.config)
}

// Save 保存配置
func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.saveLocked()
}

// saveLocked 原子写入配置文件：先写临时文件再 rename。
// Windows 服务进程每 10 秒会读一次配置，直接覆盖可能读到半截 JSON；
// rename 在同一卷上是原子替换，可以避免解析失败和脏数据。
func (cm *ConfigManager) saveLocked() error {
	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(cm.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	tmpPath := cm.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入临时配置文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, cm.filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}

	if fi, err := os.Stat(cm.filePath); err == nil {
		cm.modTime = fi.ModTime()
		cm.size = fi.Size()
	}
	return nil
}

// CheckReload 检查配置文件是否被修改，如果修改了则重新加载并触发回调。
// 同时比较修改时间和大小，规避部分文件系统 modTime 精度不足导致漏检。
func (cm *ConfigManager) CheckReload() bool {
	fi, err := os.Stat(cm.filePath)
	if err != nil {
		return false
	}

	cm.mu.RLock()
	changed := !fi.ModTime().Equal(cm.modTime) || fi.Size() != cm.size
	cm.mu.RUnlock()
	if !changed {
		return false
	}

	if err := cm.Load(); err != nil {
		fmt.Printf("[WinCron] 配置热加载失败（保留当前配置）: %v\n", err)
		return false
	}

	cm.mu.RLock()
	snapshot := cloneConfig(cm.config)
	cb := cm.onReload
	cm.mu.RUnlock()

	if cb != nil {
		cb(snapshot)
	}
	return true
}

// SetOnReload 设置重新加载回调
func (cm *ConfigManager) SetOnReload(cb func(*CrontabFile)) {
	cm.mu.Lock()
	cm.onReload = cb
	cm.mu.Unlock()
}

// validateJobID 校验任务 ID 能否安全地作为文件名/接口参数使用。
// 任务 ID 会参与 filepath.Join(logDir, id+".log") 以及 /api/jobs/:id，
// 直接改配置文件塞进 "../evil" 会把日志写到日志目录之外。
func validateJobID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("任务ID不能为空")
	}
	if id != strings.TrimSpace(id) {
		return fmt.Errorf("任务ID %q 首尾不能包含空白", id)
	}
	if utf8.RuneCountInString(id) > 128 {
		return fmt.Errorf("任务ID %q 过长（最多 128 个字符）", id)
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("任务ID %q 不能包含 '..'（ID 会参与日志文件名拼接）", id)
	}
	if strings.ContainsAny(id, "/\\:*?\"<>|") {
		return fmt.Errorf("任务ID %q 不能包含路径分隔符或 Windows 文件名非法字符 / \\ : * ? \" < > |", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("任务ID %q 不能包含控制字符", id)
		}
	}
	return nil
}

// validateJob 落盘前的统一校验。
// CLI、Web、服务三条写入路径全部经过这里，保证 crontab.json 里不会出现
// 无法解析的 cron、空命令、带路径穿越的 ID 这类脏数据。
func validateJob(job JobConfig) error {
	if err := validateJobID(job.ID); err != nil {
		return err
	}
	if strings.TrimSpace(job.Command) == "" {
		return fmt.Errorf("执行命令不能为空")
	}
	if err := cronexpr.Validate(job.Cron); err != nil {
		return fmt.Errorf("任务 %s: %w", job.ID, err)
	}
	if job.Timeout < 0 {
		return fmt.Errorf("任务 %s: 超时时间不能为负数", job.ID)
	}
	if job.MaxRetry < 0 {
		return fmt.Errorf("任务 %s: 最大重试次数不能为负数", job.ID)
	}
	return nil
}

// ValidateJobs 返回当前配置发现的全部问题，只用于提示，不阻塞加载。
// 除了逐任务的合法性，还包括 Load() 阶段发现的应用级配置问题和重复 ID ——
// 后者只有「直接编辑 crontab.json」才会出现（正常 AddJob 路径已挡住），
// 但一旦出现就会让日志、调度、手动执行按 ID 互相覆盖。
func (cm *ConfigManager) ValidateJobs() []string {
	cm.mu.RLock()
	jobs := cloneConfig(cm.config).Jobs
	problems := append([]string(nil), cm.loadIssues...)
	cm.mu.RUnlock()

	seen := make(map[string]int, len(jobs))
	for i, j := range jobs {
		if err := validateJob(j); err != nil {
			problems = append(problems, fmt.Sprintf("%s (%s): %v", j.ID, j.Name, err))
		}
		if first, ok := seen[j.ID]; ok {
			problems = append(problems, fmt.Sprintf(
				"任务ID %q 重复（第 %d 个与第 %d 个任务），日志与手动执行会互相覆盖，请改成不同 ID",
				j.ID, first+1, i+1))
		} else {
			seen[j.ID] = i
		}
	}
	return problems
}

// Path 返回配置文件路径
func (cm *ConfigManager) Path() string {
	return cm.filePath
}

// AddJob 添加任务
func (cm *ConfigManager) AddJob(job JobConfig) error {
	if err := validateJob(job); err != nil {
		return err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, j := range cm.config.Jobs {
		if j.ID == job.ID {
			return fmt.Errorf("任务ID %s 已存在", job.ID)
		}
	}

	cm.config.Jobs = append(cm.config.Jobs, cloneJob(job))
	return cm.saveLocked()
}

// UpdateJob 更新任务
func (cm *ConfigManager) UpdateJob(job JobConfig) error {
	if err := validateJob(job); err != nil {
		return err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, j := range cm.config.Jobs {
		if j.ID == job.ID {
			cm.config.Jobs[i] = cloneJob(job)
			return cm.saveLocked()
		}
	}
	return fmt.Errorf("任务ID %s 不存在", job.ID)
}

// RemoveJob 删除任务
func (cm *ConfigManager) RemoveJob(id string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	found := false
	jobs := make([]JobConfig, 0, len(cm.config.Jobs))
	for _, j := range cm.config.Jobs {
		if j.ID == id {
			found = true
			continue
		}
		jobs = append(jobs, j)
	}
	if !found {
		return fmt.Errorf("任务ID %s 不存在", id)
	}

	cm.config.Jobs = jobs
	return cm.saveLocked()
}

// GetJob 获取单个任务（副本）
func (cm *ConfigManager) GetJob(id string) (*JobConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, j := range cm.config.Jobs {
		if j.ID == id {
			jj := cloneJob(j)
			return &jj, nil
		}
	}
	return nil, fmt.Errorf("任务ID %s 不存在", id)
}

// GetJobs 获取所有任务（副本）
func (cm *ConfigManager) GetJobs() []JobConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	jobs := make([]JobConfig, len(cm.config.Jobs))
	for i, j := range cm.config.Jobs {
		jobs[i] = cloneJob(j)
	}
	return jobs
}

// GetAppConfig 获取应用配置
func (cm *ConfigManager) GetAppConfig() AppConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config.App
}
