package doctor

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strings"
	"time"
)

// EnvInfo 运行环境快照
type EnvInfo struct {
	CollectTime   string            `json:"collect_time"`
	Hostname      string            `json:"hostname"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	ProcessID     int               `json:"process_id"`
	SessionID     uint32            `json:"session_id"`
	IsService     bool              `json:"is_service"`
	IsInteractive bool              `json:"is_interactive"`
	UserName      string            `json:"user_name"`
	UserDomain    string            `json:"user_domain"`
	ExePath       string            `json:"exe_path"`
	WorkingDir    string            `json:"working_dir"`
	KeyEnvs       map[string]string `json:"key_envs"`
	Drives        []DriveInfo       `json:"drives"`
	Warnings      []string          `json:"warnings"`
	Suggestions   []string          `json:"suggestions"`
}

// DriveInfo 逻辑驱动器信息
type DriveInfo struct {
	Letter string `json:"letter"`
	Type   string `json:"type"`
	Note   string `json:"note"`
}

// Inspect 采集当前进程的运行环境
// 关键：这个函数在服务进程内被调用时，拿到的就是任务脚本的真实运行环境
func Inspect() EnvInfo {
	info := EnvInfo{
		CollectTime: time.Now().Format("2006-01-02 15:04:05"),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		ProcessID:   os.Getpid(),
		KeyEnvs:     make(map[string]string),
	}

	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	if exe, err := os.Executable(); err == nil {
		info.ExePath = exe
	}
	if wd, err := os.Getwd(); err == nil {
		info.WorkingDir = wd
	}

	// 当前运行账户
	if u, err := user.Current(); err == nil {
		info.UserName = u.Username
	}
	info.UserDomain = os.Getenv("USERDOMAIN")

	// 关键环境变量（这些在 Session 0 里和登录用户完全不同）
	for _, k := range []string{
		"USERNAME", "USERDOMAIN", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"TEMP", "TMP", "PATH", "HOMEDRIVE", "HOMEPATH", "COMPUTERNAME",
		"SESSIONNAME", "SYSTEMROOT", "WINDIR",
	} {
		if v, ok := os.LookupEnv(k); ok {
			info.KeyEnvs[k] = v
		}
	}

	// Windows 平台专属检测
	fillWindowsInfo(&info)

	// 生成风险提示
	info.Warnings, info.Suggestions = Analyze(info)

	return info
}

// Analyze 根据环境快照生成风险清单和改进建议
func Analyze(info EnvInfo) ([]string, []string) {
	var warnings []string
	var suggestions []string

	if runtime.GOOS != "windows" {
		return warnings, suggestions
	}

	if info.SessionID == 0 {
		warnings = append(warnings,
			"运行在 Session 0（服务会话）：没有桌面、没有交互式窗口，属于正常的后台运行状态")
		suggestions = append(suggestions,
			"这是服务器无人值守运行的正确状态，任务脚本无需也不应弹出任何窗口")
		suggestions = append(suggestions,
			"脚本中不要启动 GUI 程序、不要使用 pause / msg / choice 等等待用户输入的命令")
	} else {
		warnings = append(warnings,
			fmt.Sprintf("当前在 Session %d（交互式会话）中执行，与服务实际运行环境不同", info.SessionID))
		suggestions = append(suggestions,
			"此环境仅适合调试。安装为服务后，实际任务是 Session 0 环境，请用 doctor 复查")
	}

	// LocalSystem / SYSTEM 账户的典型陷阱
	u := strings.ToUpper(info.UserName)
	if strings.Contains(u, "SYSTEM") || strings.HasSuffix(u, "\\SYSTEM") {
		warnings = append(warnings,
			"服务以 LocalSystem (SYSTEM) 账户运行，HKCU / 用户配置目录 / 网络凭据与登录用户不同")
		suggestions = append(suggestions,
			"若脚本依赖用户环境，请在 crontab.json 的 app.service_user 指定服务运行账户")
	}

	// 用户目录指向 systemprofile 是典型症状
	if up := info.KeyEnvs["USERPROFILE"]; strings.Contains(strings.ToLower(up), "systemprofile") {
		warnings = append(warnings,
			"%USERPROFILE% 指向 systemprofile，脚本里用 %USERPROFILE% 找文件会失败")
		suggestions = append(suggestions,
			"改用绝对路径，或在任务配置中用 env 显式指定用户目录")
	}

	// 映射网络盘
	var remoteDrives []string
	for _, d := range info.Drives {
		if d.Type == "remote" {
			remoteDrives = append(remoteDrives, d.Letter)
		}
	}
	if len(remoteDrives) == 0 {
		warnings = append(warnings,
			"当前环境未检测到映射网络驱动器（Z: 等映射盘是按登录会话建立的，服务里通常不可见）")
		suggestions = append(suggestions,
			"备份脚本访问网络共享请改用 UNC 路径：\\\\server\\share\\dir")
		suggestions = append(suggestions,
			"或在 bat 脚本开头主动映射：net use Z: \\\\server\\share /user:账号 密码")
	} else {
		warnings = append(warnings,
			"检测到映射盘 "+strings.Join(remoteDrives, " ")+"：注意这些映射是按会话建立的，服务重启后可能失效")
	}

	// SESSIONNAME 提示
	if sn := info.KeyEnvs["SESSIONNAME"]; sn == "" || strings.EqualFold(sn, "Services") {
		warnings = append(warnings,
			"SESSIONNAME 为空或为 Services，进一步确认当前是非交互服务会话")
	}

	suggestions = append(suggestions,
		"所有命令、脚本、输出路径一律使用绝对路径；不要依赖 cmd 的当前工作目录")
	suggestions = append(suggestions,
		"用 wincron doctor -cmd \"你的命令\" 以完全相同的执行方式试跑，可提前复现问题")

	return warnings, suggestions
}

// Report 生成可读的诊断报告
func (info EnvInfo) Report() string {
	var b strings.Builder

	b.WriteString("========== WinCron 运行环境诊断 ==========\n")
	b.WriteString(fmt.Sprintf("采集时间   : %s\n", info.CollectTime))
	b.WriteString(fmt.Sprintf("主机       : %s\n", info.Hostname))
	b.WriteString(fmt.Sprintf("系统       : %s / %s\n", info.OS, info.Arch))
	b.WriteString(fmt.Sprintf("进程 PID   : %d\n", info.ProcessID))
	b.WriteString(fmt.Sprintf("Session ID : %d", info.SessionID))
	if info.SessionID == 0 {
		b.WriteString("  (Session 0 = 后台服务会话，无需登录)")
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("运行账户   : %s\n", info.UserName))
	b.WriteString(fmt.Sprintf("用户域     : %s\n", info.UserDomain))
	b.WriteString(fmt.Sprintf("可执行程序 : %s\n", info.ExePath))
	b.WriteString(fmt.Sprintf("工作目录   : %s\n", info.WorkingDir))
	b.WriteString(fmt.Sprintf("是否服务   : %v\n", info.IsService))
	b.WriteString(fmt.Sprintf("交互会话   : %v\n", info.IsInteractive))

	b.WriteString("\n--- 关键环境变量 ---\n")
	keys := make([]string, 0, len(info.KeyEnvs))
	for k := range info.KeyEnvs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := info.KeyEnvs[k]
		if k == "PATH" && len(v) > 160 {
			v = v[:160] + "... (已截断)"
		}
		b.WriteString(fmt.Sprintf("  %-14s = %s\n", k, v))
	}

	b.WriteString("\n--- 逻辑驱动器 ---\n")
	for _, d := range info.Drives {
		b.WriteString(fmt.Sprintf("  %-4s %-10s %s\n", d.Letter, d.Type, d.Note))
	}
	if len(info.Drives) == 0 {
		b.WriteString("  (未获取到)\n")
	}

	b.WriteString("\n--- 风险提示 ---\n")
	for i, w := range info.Warnings {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, w))
	}
	if len(info.Warnings) == 0 {
		b.WriteString("  无\n")
	}

	b.WriteString("\n--- 建议 ---\n")
	for i, s := range info.Suggestions {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, s))
	}
	if len(info.Suggestions) == 0 {
		b.WriteString("  无\n")
	}

	b.WriteString("=========================================\n")
	return b.String()
}
