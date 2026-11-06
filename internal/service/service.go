package service

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"wincron/internal/config"
	"wincron/internal/doctor"
	"wincron/internal/executor"
	"wincron/internal/notify"
	"wincron/internal/paths"
	"wincron/internal/scheduler"
	"wincron/internal/web"

	"github.com/kardianos/service"
)

// WinCronService WinCron服务主体
type WinCronService struct {
	configManager *config.ConfigManager
	scheduler     *scheduler.Scheduler
	executor      *executor.Executor
	notifier      *notify.Notifier
	webServer     *web.Server
	logger        service.Logger
}

// NewService 创建WinCron服务
func NewService(configPath string) (*WinCronService, error) {
	// 配置文件路径统一以 exe 目录为基准，避免服务 cwd=System32 时读错文件
	absPath := paths.Resolve(configPath)

	cm := config.NewConfigManager(absPath)
	if err := cm.Load(); err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}

	appCfg := cm.GetAppConfig()

	// 创建执行器（相对日志目录以 exe 所在目录为基准）
	exec := executor.NewExecutor(appCfg.LogDir)
	exec.StartLogCleanup(appCfg.LogRetentionDays)

	// 创建通知器并接入 SMTP 配置。
	// 此前 notify.NewNotifier() 之后没有任何地方调用 SetSMTPConfig，
	// 导致 emails 配了也永远不发；这里补齐接线。
	notifier := notify.NewNotifier()
	notifier.SetSMTPConfig(appCfg.SMTPHost, appCfg.SMTPPort, appCfg.SMTPUser, appCfg.SMTPPass)

	sched := scheduler.NewScheduler(cm, exec, notifier)
	webServer := web.NewServer(cm, sched, exec, appCfg.WebPort)

	return &WinCronService{
		configManager: cm,
		scheduler:     sched,
		executor:      exec,
		notifier:      notifier,
		webServer:     webServer,
	}, nil
}

// Program 实现kardianos/service接口
type Program struct {
	svc *WinCronService
}

// Start 服务启动
func (p *Program) Start(s service.Service) error {
	go p.run()
	return nil
}

// Stop 服务停止
func (p *Program) Stop(s service.Service) error {
	fmt.Println("[WinCron] 服务正在停止...")
	if p.svc == nil {
		return nil
	}
	// scheduler.Stop 会等已分发与手动触发的任务执行完，不必再靠 Sleep 猜时间
	p.svc.scheduler.Stop()
	if p.svc.webServer != nil {
		p.svc.webServer.Stop()
	}
	if p.svc.executor != nil {
		p.svc.executor.Close()
	}
	return nil
}

// run 服务主循环
func (p *Program) run() {
	fmt.Println("[WinCron] 服务启动中...")

	// 采集并持久化本次进程真实的运行环境。
	// 这份快照是排查"双击 bat 能跑、定时跑不了"的第一手依据，
	// 因为它记录的是服务进程（Session 0）看到的世界，而非管理员终端看到的。
	envInfo := doctor.Inspect()
	fmt.Print(envInfo.Report())
	writeEnvSnapshot(envInfo, p.svc.configManager.GetAppConfig().LogDir)

	if problems := p.svc.configManager.ValidateJobs(); len(problems) > 0 {
		fmt.Println("[WinCron] 警告: 以下任务配置不合法，将不会被调度：")
		for _, problem := range problems {
			fmt.Printf("  - %s\n", problem)
		}
	}

	if err := p.svc.scheduler.Start(); err != nil {
		fmt.Printf("[WinCron] 调度器启动失败: %v\n", err)
		return
	}

	appCfg := p.svc.configManager.GetAppConfig()
	if appCfg.WebEnabled {
		go func() {
			fmt.Printf("[WinCron] Web管理界面启动在端口 %d\n", appCfg.WebPort)
			if err := p.svc.webServer.Start(); err != nil && err != http.ErrServerClosed {
				fmt.Printf("[WinCron] Web服务器启动失败: %v\n", err)
			}
		}()
	}

	fmt.Println("[WinCron] 服务已启动，等待任务调度...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
}

// GetServiceConfig 获取服务配置
func GetServiceConfig(configPath string) service.Config {
	cm := config.NewConfigManager(configPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("[WinCron] 警告: 读取配置失败，服务将使用默认参数: %v\n", err)
	}
	appCfg := cm.GetAppConfig()

	cfg := service.Config{
		Name:        appCfg.ServiceName,
		DisplayName: appCfg.DisplayName,
		Description: appCfg.Description,
		Arguments:   []string{"-run", "-config", configPath},
		Option:      service.KeyValue{},
	}

	// 支持以指定用户账户运行服务。
	// 这是解决 Session 0 环境差异最有效的手段：
	// 服务以该用户身份运行后，任务脚本继承其 HKCU、环境变量和网络凭据，
	// 避免"双击 bat 能跑、定时跑不了"。
	if appCfg.ServiceUser != "" {
		cfg.UserName = appCfg.ServiceUser
		if appCfg.ServicePassword != "" {
			cfg.Option["Password"] = appCfg.ServicePassword
		}
	}

	return cfg
}

// RunAsService 以Windows服务方式运行
func RunAsService(configPath string) error {
	svc, err := NewService(configPath)
	if err != nil {
		return err
	}

	prog := &Program{svc: svc}
	svcConfig := GetServiceConfig(configPath)

	s, err := service.New(prog, &svcConfig)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}

	logger, err := s.Logger(nil)
	if err != nil {
		log.Printf("[WinCron] 无法获取服务日志: %v\n", err)
	} else {
		prog.svc.logger = logger
	}

	if err := s.Run(); err != nil {
		return fmt.Errorf("服务运行失败: %w", err)
	}
	return nil
}

// RunForeground 前台运行（调试用）
func RunForeground(configPath string) error {
	svc, err := NewService(configPath)
	if err != nil {
		return err
	}
	(&Program{svc: svc}).run()
	return nil
}

// InstallService 安装服务
func InstallService(configPath string) error {
	absPath := paths.Resolve(configPath)

	cm := config.NewConfigManager(absPath)
	if err := cm.Load(); err != nil {
		fmt.Printf("[WinCron] 配置加载警告: %v，将使用默认配置\n", err)
	}

	svcConfig := GetServiceConfig(absPath)

	exePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		return fmt.Errorf("无法获取可执行文件路径: %w", err)
	}

	svcConfig.Executable = exePath
	svcConfig.Arguments = []string{"-run", "-config", absPath}

	s, err := service.New(&Program{}, &svcConfig)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}
	if err := s.Install(); err != nil {
		return fmt.Errorf("安装服务失败: %w", err)
	}

	fmt.Printf("[WinCron] 服务 '%s' 已成功安装\n", svcConfig.DisplayName)
	fmt.Printf("[WinCron] 服务名称: %s\n", svcConfig.Name)
	fmt.Printf("[WinCron] 可执行文件: %s\n", exePath)
	fmt.Printf("[WinCron] 配置文件: %s\n", absPath)
	fmt.Println("[WinCron] 使用以下命令启动服务: net start " + svcConfig.Name)
	fmt.Println("[WinCron] 或使用: wincron start")
	return nil
}

// UninstallService 卸载服务
func UninstallService(configPath string) error {
	absPath := paths.Resolve(configPath)
	svcConfig := GetServiceConfig(absPath)
	s, err := service.New(&Program{}, &svcConfig)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}
	if err := s.Uninstall(); err != nil {
		return fmt.Errorf("卸载服务失败: %w", err)
	}
	fmt.Printf("[WinCron] 服务 '%s' 已成功卸载\n", svcConfig.DisplayName)
	return nil
}

// StartService 启动服务
func StartService(configPath string) error {
	absPath := paths.Resolve(configPath)
	svcConfig := GetServiceConfig(absPath)
	s, err := service.New(&Program{}, &svcConfig)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("启动服务失败: %w", err)
	}
	fmt.Printf("[WinCron] 服务 '%s' 已启动\n", svcConfig.DisplayName)
	return nil
}

// StopService 停止服务
func StopService(configPath string) error {
	absPath := paths.Resolve(configPath)
	svcConfig := GetServiceConfig(absPath)
	s, err := service.New(&Program{}, &svcConfig)
	if err != nil {
		return fmt.Errorf("创建服务失败: %w", err)
	}
	if err := s.Stop(); err != nil {
		return fmt.Errorf("停止服务失败: %w", err)
	}
	fmt.Printf("[WinCron] 服务 '%s' 已停止\n", svcConfig.DisplayName)
	return nil
}

// RestartService 重启服务
func RestartService(configPath string) error {
	if err := StopService(configPath); err != nil {
		fmt.Printf("[WinCron] 停止服务警告: %v\n", err)
	}
	time.Sleep(2 * time.Second)
	return StartService(configPath)
}

// ServiceStatus 获取服务状态
func ServiceStatus(configPath string) (string, error) {
	absPath := paths.Resolve(configPath)
	svcConfig := GetServiceConfig(absPath)
	s, err := service.New(&Program{}, &svcConfig)
	if err != nil {
		return "", fmt.Errorf("创建服务失败: %w", err)
	}
	status, err := s.Status()
	if err != nil {
		// 安装前执行 wincron status 是正常操作，kardianos/service 会返回
		// ErrNotInstalled。以前把它当致命错误直接退出，导致用户装服务前
		// 连「服务名/配置文件/Web端口」都看不到。
		if errors.Is(err, service.ErrNotInstalled) {
			return "not_installed", nil
		}
		return "", fmt.Errorf("获取服务状态失败: %w", err)
	}
	switch status {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}
