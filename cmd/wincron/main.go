package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wincron/internal/config"
	"wincron/internal/cronexpr"
	"wincron/internal/doctor"
	"wincron/internal/executor"
	"wincron/internal/paths"
	"wincron/internal/service"
)

// version 由发布工作流通过 -ldflags "-X main.version=<tag 去掉 v>" 注入。
// 本地直接 go build 时显示 1.0.0。
var version = "1.0.0"

var (
	valueFlags = map[string]bool{
		"-config": true, "--config": true,
		"-cmd": true, "--cmd": true,
	}
	boolFlags = map[string]bool{
		"-run": true, "--run": true,
		"-h": true, "--help": true,
	}
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	fs := flag.NewFlagSet("wincron", flag.ContinueOnError)
	var configPath string
	var testCommand string
	fs.StringVar(&configPath, "config", "crontab.json", "配置文件路径")
	fs.StringVar(&testCommand, "cmd", "", "doctor 专用：用与服务相同的执行方式试跑一条命令")
	fs.Bool("run", false, "以服务模式运行（内部使用）")

	// Go 标准 flag 遇到第一个位置参数就停止解析，导致
	//   wincron remove job-1 -config D:\crontab.json
	// 这类写法里 -config 被当成位置参数。这里把 flag 统一排到前面再解析。
	if err := fs.Parse(reorderArgs(os.Args[2:])); err != nil {
		os.Exit(1)
	}
	posArgs := fs.Args()

	// 所有子命令统一使用绝对路径。服务由 SCM 启动时 cwd 是 System32，
	// 若 add/list/logs 等命令各自按 cwd 解析，用户编辑的配置和服务读取的
	// 就不是同一份文件。getConfigAbsPath 对已是绝对路径的入参是幂等的。
	configPath = getConfigAbsPath(configPath)

	switch command {
	case "-run":
		runAsService(configPath) // 由 SCM 调用
	case "install":
		installService(configPath)
	case "uninstall":
		uninstallService(configPath)
	case "start":
		startService(configPath)
	case "stop":
		stopService(configPath)
	case "restart":
		restartService(configPath)
	case "status":
		statusService(configPath)
	case "run", "foreground":
		runForeground(configPath)
	case "list", "ls":
		listJobs(configPath)
	case "add":
		addJob(configPath)
	case "remove", "rm":
		removeJob(configPath, posArgs)
	case "enable":
		toggleJob(configPath, posArgs, true)
	case "disable":
		toggleJob(configPath, posArgs, false)
	case "reload":
		reloadConfig(configPath)
	case "logs":
		showLogs(configPath, posArgs)
	case "doctor":
		runDoctor(configPath, testCommand)
	case "validate":
		validateConfig(configPath)
	case "notify":
		notifyCommand(configPath, posArgs)
	case "version":
		fmt.Printf("WinCron v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Printf("未知命令: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

// reorderArgs 把所有已知 flag（含等号形式）提到位置参数之前，
// 同时保证带值 flag 和它的值相邻。位置参数原样保留顺序。
func reorderArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case valueFlags[a]:
			flags = append(flags, a)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case boolFlags[a]:
			flags = append(flags, a)
		case strings.HasPrefix(a, "-") && strings.Contains(a, "="):
			flags = append(flags, a)
		default:
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

// ---------- 交互式输入 ----------
//
// 这里必须逐行读取，绝不能用 fmt.Scanln：
// Scanln 按空白切分，输入 "* * * * *" 只会取到第一个 "*"，
// 剩下 4 个 "*" 留在缓冲区，被后面几个字段依次误读，
// 最终写成 cron:"*"、work_dir:"*"、command 丢失 —— 正是用户反馈的 bug。

func newInputReader() *bufio.Reader {
	return bufio.NewReader(os.Stdin)
}

// readLine 读取完整一行，保留行内空格，仅去除首尾空白与 CRLF
func readLine(r *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	return strings.TrimSpace(line)
}

// readInt 读取非负整数，空输入用默认值，错误输入最多重试 3 次
func readInt(r *bufio.Reader, prompt string, def int) int {
	for i := 0; i < 3; i++ {
		s := readLine(r, prompt)
		if s == "" {
			return def
		}
		v, err := strconv.Atoi(s)
		if err == nil && v >= 0 {
			return v
		}
		fmt.Println("  请输入非负整数（直接回车使用默认值）")
	}
	return def
}

func printUsage() {
	fmt.Printf(`WinCron - Windows定时任务管理工具 v%s
替代Windows计划任务的后台cron服务

用法:
  wincron <命令> [选项] [参数]

服务管理命令:
  install     安装为Windows服务（开机自启，无需登录）
  uninstall   卸载Windows服务
  start       启动服务
  stop        停止服务
  restart     重启服务
  status      查看服务状态

运行命令:
  run         前台运行（调试模式，不注册为服务）
  reload      立即触发服务重载（只更新文件时间戳，不改内容）
  validate    检查配置文件里的任务是否合法
  notify      查看通知渠道状态 / 测试发送 / 列出支持的渠道类型

任务管理命令:
  list        列出所有任务，并提示配置问题
  add         添加任务（交互式，cron 和命令都可含空格）
  remove      删除任务  示例: wincron remove <任务ID>
  enable      启用任务  示例: wincron enable <任务ID>
  disable     禁用任务  示例: wincron disable <任务ID>
  logs        查看执行日志  示例: wincron logs [任务ID]
  doctor      诊断运行环境（Session / 账户 / 映射盘 / 风险清单）

其他命令:
  version     显示版本信息
  help        显示帮助信息

选项:
  -config string  指定配置文件路径（默认: exe 同目录下的 crontab.json）
  -cmd string     doctor 专用：以与服务完全相同的执行方式试跑一条命令

Cron 表达式:
  5 字段（Linux crontab 写法）: * * * * *        每分钟
  6 字段（秒级）:              */10 * * * * *    每10秒
  两种写法自动兼容，@every 1m / @daily 等描述符同样可用。

示例:
  wincron install
  wincron start
  wincron add                              # 交互式，命令可直接写 echo aaa >> c:\b.txt
  wincron list
  wincron doctor -cmd "C:\scripts\backup.bat"
  wincron remove job-1 -config D:\crontab.json

Web管理界面:
  服务启动后访问 http://localhost:9090
  默认账号: admin / CHANGE_ME_BEFORE_USE（首次使用请立即修改 crontab.json）
`, version)
}

// getConfigAbsPath 解析配置路径。
// 默认值以 exe 所在目录为基准：Windows 服务由 SCM 启动时 cwd 是 System32，
// 若用 filepath.Abs("crontab.json")，服务读的是 System32 里的文件，和用户编辑的不是同一份。
func getConfigAbsPath(configPath string) string {
	if configPath == "" || configPath == "crontab.json" {
		return paths.DefaultConfigPath()
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Printf("错误: 无法获取配置文件绝对路径: %v\n", err)
		os.Exit(1)
	}
	return abs
}

func installService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.InstallService(absPath); err != nil {
		fmt.Printf("安装服务失败: %v\n", err)
		os.Exit(1)
	}
}

func uninstallService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.UninstallService(absPath); err != nil {
		fmt.Printf("卸载服务失败: %v\n", err)
		os.Exit(1)
	}
}

func startService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.StartService(absPath); err != nil {
		fmt.Printf("启动服务失败: %v\n", err)
		os.Exit(1)
	}
}

func stopService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.StopService(absPath); err != nil {
		fmt.Printf("停止服务失败: %v\n", err)
		os.Exit(1)
	}
}

func restartService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.RestartService(absPath); err != nil {
		fmt.Printf("重启服务失败: %v\n", err)
		os.Exit(1)
	}
}

func statusService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	status, err := service.ServiceStatus(absPath)
	if err != nil {
		fmt.Printf("获取服务状态失败: %v\n", err)
		os.Exit(1)
	}

	appCfg := loadAppConfig(absPath)
	fmt.Println("=== WinCron 服务状态 ===")
	fmt.Printf("服务名称: %s\n", appCfg.ServiceName)
	fmt.Printf("显示名称: %s\n", appCfg.DisplayName)
	fmt.Printf("服务状态: ")
	switch status {
	case "running":
		fmt.Println("运行中")
	case "stopped":
		fmt.Println("已停止")
	case "not_installed":
		fmt.Println("未安装（以管理员身份执行 wincron install 安装服务）")
	default:
		fmt.Println("未知")
	}
	if appCfg.WebEnabled {
		fmt.Printf("Web管理: http://localhost:%d\n", appCfg.WebPort)
	}
	fmt.Printf("配置文件: %s\n", absPath)
}

func runAsService(configPath string) {
	absPath := getConfigAbsPath(configPath)
	if err := service.RunAsService(absPath); err != nil {
		fmt.Printf("服务运行失败: %v\n", err)
		os.Exit(1)
	}
}

func runForeground(configPath string) {
	absPath := getConfigAbsPath(configPath)
	fmt.Println("=== WinCron 前台运行（调试模式）===")
	fmt.Printf("配置文件: %s\n", absPath)
	fmt.Println("按 Ctrl+C 停止")
	fmt.Println()
	if err := service.RunForeground(absPath); err != nil {
		fmt.Printf("运行失败: %v\n", err)
		os.Exit(1)
	}
}

func loadAppConfig(configPath string) config.AppConfig {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		return config.DefaultConfig().App
	}
	return cm.GetAppConfig()
}

func listJobs(configPath string) {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	jobs := cm.GetJobs()
	if len(jobs) == 0 {
		fmt.Println("暂无任务")
		return
	}

	fmt.Println("=== 任务列表 ===")
	fmt.Printf("%-20s %-8s %-20s %-22s %-8s %-40s\n", "ID", "状态", "名称", "Cron", "超时", "命令")
	fmt.Println(strings.Repeat("-", 124))
	for _, j := range jobs {
		status := "禁用"
		if j.Enabled {
			status = "启用"
		}
		timeout := fmt.Sprintf("%ds", j.Timeout)
		if j.Timeout == 0 {
			timeout = "不限"
		}
		cmd := j.Command
		if len(cmd) > 40 {
			cmd = cmd[:37] + "..."
		}
		fmt.Printf("%-20s %-8s %-20s %-22s %-8s %-40s\n", j.ID, status, j.Name, j.Cron, timeout, cmd)
	}
	fmt.Printf("\n共 %d 个任务\n", len(jobs))

	if problems := cm.ValidateJobs(); len(problems) > 0 {
		fmt.Println("\n警告: 配置检查发现以下问题：")
		for _, p := range problems {
			fmt.Printf("  - %s\n", p)
		}
		fmt.Println("  可用 wincron validate 单独检查，或直接编辑配置文件")
	}
}

func validateConfig(configPath string) {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}
	problems := cm.ValidateJobs()
	if len(problems) == 0 {
		fmt.Printf("配置校验通过，共 %d 个任务均合法。\n", len(cm.GetJobs()))
		return
	}
	fmt.Printf("发现 %d 个问题：\n", len(problems))
	for _, p := range problems {
		fmt.Printf("  - %s\n", p)
	}
	os.Exit(1)
}

// nextJobID 找一个未被占用的最小编号，避免 len(jobs)+1 与已有 ID 撞车
func nextJobID(jobs []config.JobConfig) string {
	used := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		used[j.ID] = true
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("job-%d", i)
		if !used[id] {
			return id
		}
	}
}

// addInput 是交互式 add 收集到的原始输入。
// 抽成结构 + 纯函数，便于用测试回放整行输入，保证 cron 与命令中的空格、
// 重定向符不会被按空白切分丢失。
type addInput struct {
	ID       string
	Name     string
	Cron     string
	Command  string
	WorkDir  string
	Timeout  int
	MaxRetry int
	Enabled  bool
}

func collectAddInput(r *bufio.Reader, defaultID string) addInput {
	in := addInput{}
	in.ID = readLine(r, "任务ID（回车自动生成）: ")
	if in.ID == "" {
		in.ID = defaultID
	}
	in.Name = readLine(r, "任务名称: ")
	if in.Name == "" {
		in.Name = in.ID
	}
	in.Cron = readLine(r, "Cron表达式（5或6字段，例: * * * * *）: ")
	in.Command = readLine(r, "执行命令（可含空格/重定向）: ")
	in.WorkDir = readLine(r, "工作目录（可选，回车跳过）: ")
	in.Timeout = readInt(r, "超时时间秒（0=不超时，回车默认0）: ", 0)
	in.MaxRetry = readInt(r, "最大重试次数（0=不重试，回车默认0）: ", 0)
	en := readLine(r, "是否启用（y/n，回车默认y）: ")
	in.Enabled = !strings.EqualFold(en, "n") && !strings.EqualFold(en, "no")
	return in
}

func buildJobFromInput(in addInput) config.JobConfig {
	return config.JobConfig{
		ID:       in.ID,
		Name:     in.Name,
		Cron:     in.Cron,
		Command:  in.Command,
		WorkDir:  in.WorkDir,
		Enabled:  in.Enabled,
		Timeout:  in.Timeout,
		MaxRetry: in.MaxRetry,
		Notify: config.NotifyConfig{
			OnSuccess: false,
			OnFailure: false,
			Channels:  []string{},
		},
		Env: map[string]string{},
	}
}

func addJob(configPath string) {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== 添加定时任务 ===")
	fmt.Println("提示: cron 和命令都可以包含空格，输入完整一行后回车即可")
	fmt.Println(`      例: * * * * *        例: echo aaa >> c:\b.txt`)
	fmt.Println()

	in := collectAddInput(newInputReader(), nextJobID(cm.GetJobs()))

	// 落盘前先校验，给出可读的中文错误，不写脏数据
	if err := cronexpr.Validate(in.Cron); err != nil {
		fmt.Printf("\n输入有误: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(in.Command) == "" {
		fmt.Println("\n输入有误: 执行命令不能为空")
		os.Exit(1)
	}

	job := buildJobFromInput(in)
	if err := cm.AddJob(job); err != nil {
		fmt.Printf("\n添加失败: %v\n", err)
		os.Exit(1)
	}

	normalized, _ := cronexpr.Parse(in.Cron)
	fmt.Printf("\n任务 '%s' 已写入配置文件\n", in.Name)
	fmt.Printf("  ID      : %s\n", in.ID)
	fmt.Printf("  Cron    : %s", in.Cron)
	if normalized != in.Cron {
		fmt.Printf("   -> 实际按 %s 调度", normalized)
	}
	fmt.Println()
	fmt.Printf("  命令    : %s\n", in.Command)
	if in.WorkDir != "" {
		fmt.Printf("  工作目录: %s\n", in.WorkDir)
	}
	fmt.Printf("  超时    : %d 秒 | 重试: %d | 启用: %v\n", in.Timeout, in.MaxRetry, in.Enabled)
	fmt.Printf("  文件    : %s\n", configPath)
	fmt.Println("\n服务运行中时，10 秒内自动热加载；也可执行 wincron reload 立即触发")
}

func removeJob(configPath string, args []string) {
	if len(args) < 1 {
		args = []string{readLine(newInputReader(), "请输入任务ID: ")}
	}
	if strings.TrimSpace(args[0]) == "" {
		fmt.Println("错误: 任务ID不能为空")
		os.Exit(1)
	}

	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}
	if err := cm.RemoveJob(args[0]); err != nil {
		fmt.Printf("删除失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("任务 %s 已删除\n", args[0])
}

func toggleJob(configPath string, args []string, enable bool) {
	if len(args) < 1 {
		args = []string{readLine(newInputReader(), "请输入任务ID: ")}
	}

	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}
	job, err := cm.GetJob(args[0])
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}

	job.Enabled = enable
	if err := cm.UpdateJob(*job); err != nil {
		fmt.Printf("更新失败: %v\n", err)
		os.Exit(1)
	}

	status := "启用"
	if !enable {
		status = "禁用"
	}
	fmt.Printf("任务 %s 已%s\n", job.Name, status)
}

// reloadConfig 通过只更新文件修改时间（不改内容）触发服务热加载。
func reloadConfig(configPath string) {
	if _, err := os.Stat(configPath); err != nil {
		fmt.Printf("配置文件不存在: %s\n", configPath)
		os.Exit(1)
	}
	now := time.Now()
	if err := os.Chtimes(configPath, now, now); err != nil {
		fmt.Printf("触发重载失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("已发出重载信号（仅更新文件时间戳，配置内容未改动）")
	fmt.Println("服务将在 10 秒内检测到并重新加载任务")
}

// runDoctor 诊断运行环境，可选地用与服务完全相同的执行路径试跑一条命令
func runDoctor(configPath string, testCommand string) {
	fmt.Print(doctor.Inspect().Report())

	if testCommand == "" {
		fmt.Println("\n提示: 可用 -cmd 参数试跑脚本，验证它在这套执行方式下能否正常工作")
		fmt.Println("  例: wincron doctor -cmd \"C:\\scripts\\backup.bat\"")
		fmt.Println("\n注意: 若当前在管理员终端执行，看到的是 Session 1 环境。")
		fmt.Println("      安装为服务后的真实环境，请查看 logs/environment.log 或 Web 的「运行环境」页。")
		return
	}

	fmt.Printf("\n========== 试跑命令 ==========\n命令: %s\n", testCommand)
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("[警告] 配置加载失败: %v，日志目录将使用默认值\n", err)
	}
	app := cm.GetAppConfig()
	exec := executor.NewExecutor(app.LogDir)
	exec.SetLimits(app.LogMaxEntries, int64(app.LogMaxFileMB)<<20)
	defer exec.Close()
	job := &config.JobConfig{
		ID:       "doctor-test",
		Name:     "环境预检",
		Command:  testCommand,
		Enabled:  true,
		Timeout:  60,
		MaxRetry: 0,
		Env:      map[string]string{},
	}

	result := exec.Execute(job)
	fmt.Printf("开始时间: %s\n", result.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("结束时间: %s\n", result.EndTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("耗时: %.2f 秒 | 退出码: %d | 结果: ", result.Duration, result.ExitCode)
	if result.Success {
		fmt.Println("成功")
	} else {
		fmt.Println("失败")
	}
	if result.Output != "" {
		fmt.Printf("\n--- 标准输出 ---\n%s\n", result.Output)
	}
	if result.Error != "" {
		fmt.Printf("\n--- 错误输出 ---\n%s\n", result.Error)
	}
	fmt.Println("==============================")

	if !result.Success {
		fmt.Println("\n诊断: 命令执行失败。请重点检查绝对路径、网络共享 UNC 写法、以及脚本是否依赖用户会话资源。")
		os.Exit(1)
	}
	fmt.Println("\n诊断: 命令执行成功。该命令可作为 WinCron 任务使用（服务环境下仍建议复查 environment.log）。")
}

func showLogs(configPath string, args []string) {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	logDir := paths.Resolve(cm.GetAppConfig().LogDir)

	if len(args) > 0 {
		id := args[0]
		// 任务 ID 会拼进日志文件路径，命令行的 ID 同样不能带路径分隔符
		if id == "" || id == "." || id == ".." || strings.Contains(id, "..") ||
			strings.ContainsAny(id, "/\\:*?\"<>|") {
			fmt.Printf("读取日志失败: 非法的任务ID %q（不能包含路径分隔符或 '..'）\n", id)
			os.Exit(1)
		}
		if _, err := cm.GetJob(id); err != nil {
			fmt.Printf("读取日志失败: %v\n", err)
			os.Exit(1)
		}

		logFile := filepath.Join(logDir, id+".log")
		data, err := os.ReadFile(logFile)
		if err != nil {
			if os.IsNotExist(err) {
				// 任务已配置但还没跑过（或日志被清理）不是错误，退出码应为 0
				fmt.Printf("=== 任务 %s 的日志 ===\n任务尚未执行，或日志已被清理，暂无执行日志。\n", id)
				fmt.Printf("日志文件: %s\n", logFile)
				return
			}
			fmt.Printf("读取日志失败: %v\n", err)
			fmt.Printf("日志文件: %s\n", logFile)
			os.Exit(1)
		}
		fmt.Printf("=== 任务 %s 的日志 ===\n\n", id)
		fmt.Print(string(data))
	} else {
		entries, err := os.ReadDir(logDir)
		if err != nil {
			fmt.Printf("读取日志目录失败: %v\n", err)
			fmt.Printf("日志目录: %s\n", logDir)
			os.Exit(1)
		}
		fmt.Printf("=== 日志文件列表（%s）===\n", logDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".log") {
				fmt.Println(filepath.Join(logDir, e.Name()))
			}
		}
		fmt.Println("\n使用 'wincron logs <任务ID>' 查看具体任务日志")
	}
}
