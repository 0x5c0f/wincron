package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"wincron/internal/config"
	"wincron/internal/cronexpr"
	"wincron/internal/doctor"
	"wincron/internal/executor"
	"wincron/internal/notify"
	"wincron/internal/paths"
	"wincron/internal/scheduler"

	"github.com/gin-gonic/gin"
)

// Server Web管理服务器
type Server struct {
	configManager *config.ConfigManager
	scheduler     *scheduler.Scheduler
	executor      *executor.Executor
	notifier      *notify.Notifier
	port          int
	server        *http.Server

	sessionKey  []byte              // 进程级随机会话主密钥，不落盘
	limiter     *loginLimiter       // 登录失败限流
	revocations *sessionRevocations // 主动登出的令牌黑名单
}

// NewServer 创建Web服务器
func NewServer(cm *config.ConfigManager, sched *scheduler.Scheduler, exec *executor.Executor, notifier *notify.Notifier, port int) *Server {
	return &Server{
		configManager: cm,
		scheduler:     sched,
		executor:      exec,
		notifier:      notifier,
		port:          port,
		sessionKey:    newSessionKey(),
		limiter:       newLoginLimiter(),
		revocations:   newSessionRevocations(),
	}
}

// newRouter 构建完整路由表。抽成独立方法后，单元测试可以直接用 httptest
// 打含认证中间件的真实路由，不必监听端口。
func (s *Server) newRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// 静态扩展目录以 exe 目录为基准，避免服务 cwd=System32 时路径失效
	r.Static("/static", paths.Resolve("web/static"))

	// 浏览器会自动请求 /favicon.ico，若不处理控制台会多一条 404 错误。
	// 内嵌一个 SVG 图标直接返回，无需额外静态文件。
	r.GET("/favicon.ico", s.handleFavicon)

	// 认证相关端点必须公开，因此注册在 /api group 之外：
	// Gin 只把 group 的 handler chain 合并到 group 自己注册的路由上，
	// 这些路由不会经过 authMiddleware，避免「查登录态也要先登录」的死循环。
	r.GET("/login", s.handleLoginPage)
	r.POST("/api/login", s.handleLogin)
	r.POST("/api/logout", s.handleLogout)
	r.GET("/api/auth/status", s.handleAuthStatus)

	r.GET("/", s.handleIndex)
	r.GET("/index.html", s.handleIndex)

	api := r.Group("/api", s.authMiddleware())
	{
		api.GET("/status", s.handleStatus)

		api.GET("/jobs", s.handleListJobs)
		api.POST("/jobs", s.handleAddJob)
		api.GET("/jobs/:id", s.handleGetJob)
		api.PUT("/jobs/:id", s.handleUpdateJob)
		api.DELETE("/jobs/:id", s.handleDeleteJob)
		api.POST("/jobs/:id/run", s.handleRunJob)
		api.POST("/jobs/:id/toggle", s.handleToggleJob)

		api.GET("/logs", s.handleGetLogs)
		api.GET("/logs/:jobId", s.handleGetJobLogs)

		api.GET("/schedule/info", s.handleScheduleInfo)
		api.POST("/schedule/reload", s.handleReloadSchedule)

		api.GET("/doctor", s.handleDoctor)
		api.GET("/validate", s.handleValidate)

		api.GET("/notify/channels", s.handleNotifyChannels)
		api.POST("/notify/channels/:name/test", s.handleTestNotifyChannel)
	}
	return r
}

// Start 启动Web服务器
func (s *Server) Start() error {
	gin.SetMode(gin.ReleaseMode)
	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.newRouter(),
	}

	fmt.Printf("[WinCron] Web管理界面监听: http://localhost:%d\n", s.port)
	if s.authEnabled() {
		fmt.Printf("[WinCron] 登录页面: http://localhost:%d/login （默认账号见 crontab.json）\n", s.port)
	}
	return s.server.ListenAndServe()
}

// Stop 停止Web服务器
func (s *Server) Stop() {
	if s.server != nil {
		_ = s.server.Close()
	}
}

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#667eea"/><stop offset="1" stop-color="#764ba2"/></linearGradient></defs><rect width="64" height="64" rx="14" fill="url(#g)"/><text x="32" y="45" font-family="Segoe UI,Arial,sans-serif" font-size="38" font-weight="700" text-anchor="middle" fill="#fff">W</text></svg>`

func (s *Server) handleFavicon(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", []byte(faviconSVG))
}

// handleIndex 返回管理界面HTML。
// 服务端直接做登录态重定向：未登录访问主页时跳到独立登录页。
// no-store 避免浏览器把已登录页面缓存下来，导致登出后按「后退」还能看到内容。
func (s *Server) handleIndex(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if s.authEnabled() {
		if _, ok := s.sessionUser(c); !ok {
			c.Redirect(http.StatusFound, "/login")
			return
		}
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(getIndexHTML()))
}

// handleStatus 获取服务状态
func (s *Server) handleStatus(c *gin.Context) {
	running := s.scheduler.IsRunning()
	jobs := s.configManager.GetJobs()
	enabledCount := 0
	for _, j := range jobs {
		if j.Enabled {
			enabledCount++
		}
	}

	logs := s.executor.GetAllLogs(1)
	lastRunTime := "无"
	if len(logs) > 0 {
		lastRunTime = logs[0].Time.Format("2006-01-02 15:04:05")
	}

	c.JSON(http.StatusOK, gin.H{
		"status":        map[string]bool{"running": running},
		"total_jobs":    len(jobs),
		"enabled_jobs":  enabledCount,
		"running_jobs":  s.scheduler.GetRunningJobs(),
		"last_run_time": lastRunTime,
		"current_time":  time.Now().Format("2006-01-02 15:04:05"),
		"config_issues": s.configManager.ValidateJobs(),
	})
}

// handleListJobs 列出所有任务
func (s *Server) handleListJobs(c *gin.Context) {
	jobs := s.configManager.GetJobs()
	scheduledJobs := s.scheduler.ListScheduledJobs()

	scheduleMap := make(map[string]scheduler.ScheduledJobInfo)
	for _, sj := range scheduledJobs {
		scheduleMap[sj.JobID] = sj
	}

	type JobWithSchedule struct {
		config.JobConfig
		NextRun string `json:"next_run"`
		PrevRun string `json:"prev_run"`
	}

	result := make([]JobWithSchedule, 0, len(jobs))
	for _, job := range jobs {
		jws := JobWithSchedule{JobConfig: job}
		if sj, ok := scheduleMap[job.ID]; ok {
			if !sj.Next.IsZero() {
				jws.NextRun = sj.Next.Format("2006-01-02 15:04:05")
			}
			if !sj.Prev.IsZero() {
				jws.PrevRun = sj.Prev.Format("2006-01-02 15:04:05")
			}
		}
		result = append(result, jws)
	}

	c.JSON(http.StatusOK, gin.H{"jobs": result, "config_issues": s.configManager.ValidateJobs()})
}

// validateJobInput Web 侧前置校验，保证非法 cron 不会先落盘再报错
func validateJobInput(job config.JobConfig) error {
	if err := cronexpr.Validate(job.Cron); err != nil {
		return err
	}
	if strings.TrimSpace(job.Command) == "" {
		return fmt.Errorf("执行命令不能为空")
	}
	return nil
}

// handleAddJob 添加任务
func (s *Server) handleAddJob(c *gin.Context) {
	var job config.JobConfig
	if err := c.ShouldBindJSON(&job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	if err := validateJobInput(job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// config.AddJob 内部还会再校验一次，落盘与内存状态不会不一致
	if err := s.configManager.AddJob(job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if job.Enabled {
		if err := s.scheduler.AddJob(&job); err != nil {
			// 配置已合法保存，只是调度器未运行；服务启动/热加载会补上，不算失败
			c.JSON(http.StatusOK, gin.H{
				"message": "任务已保存，但暂未加入调度器；服务启动或重载后会自动生效",
				"warning": err.Error(),
				"job":     job,
			})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "任务添加成功", "job": job})
}

// handleGetJob 获取单个任务
func (s *Server) handleGetJob(c *gin.Context) {
	job, err := s.configManager.GetJob(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"job": job})
}

// handleUpdateJob 更新任务
func (s *Server) handleUpdateJob(c *gin.Context) {
	id := c.Param("id")
	var job config.JobConfig
	if err := c.ShouldBindJSON(&job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	job.ID = id
	if err := validateJobInput(job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.UpdateJob(job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_ = s.scheduler.Reload()
	c.JSON(http.StatusOK, gin.H{"message": "任务更新成功", "job": job})
}

// handleDeleteJob 删除任务
func (s *Server) handleDeleteJob(c *gin.Context) {
	id := c.Param("id")
	_ = s.scheduler.RemoveJob(id)
	if err := s.configManager.RemoveJob(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "任务删除成功"})
}

// handleRunJob 手动执行任务
func (s *Server) handleRunJob(c *gin.Context) {
	if err := s.scheduler.RunOnce(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "任务已触发执行"})
}

// handleToggleJob 启用/禁用任务
func (s *Server) handleToggleJob(c *gin.Context) {
	id := c.Param("id")
	job, err := s.configManager.GetJob(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	job.Enabled = !job.Enabled
	if err := s.configManager.UpdateJob(*job); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_ = s.scheduler.Reload()
	status := "启用"
	if !job.Enabled {
		status = "禁用"
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("任务已%s", status), "enabled": job.Enabled})
}

// handleGetLogs 获取日志
func (s *Server) handleGetLogs(c *gin.Context) {
	limit := 50
	_, _ = fmt.Sscanf(c.DefaultQuery("limit", "50"), "%d", &limit)
	logs := s.executor.GetAllLogs(limit)
	c.JSON(http.StatusOK, gin.H{"logs": logs, "count": len(logs)})
}

// handleGetJobLogs 获取指定任务的日志
func (s *Server) handleGetJobLogs(c *gin.Context) {
	limit := 50
	_, _ = fmt.Sscanf(c.DefaultQuery("limit", "50"), "%d", &limit)
	logs := s.executor.GetLogs(c.Param("jobId"), limit)
	c.JSON(http.StatusOK, gin.H{"logs": logs, "count": len(logs)})
}

// handleScheduleInfo 获取调度信息
func (s *Server) handleScheduleInfo(c *gin.Context) {
	jobs := s.scheduler.ListScheduledJobs()
	c.JSON(http.StatusOK, gin.H{"scheduled_jobs": jobs, "count": len(jobs)})
}

// handleReloadSchedule 重新加载调度
func (s *Server) handleReloadSchedule(c *gin.Context) {
	if err := s.scheduler.Reload(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "调度器已重新加载"})
}

// handleDoctor 返回服务进程真实的运行环境
func (s *Server) handleDoctor(c *gin.Context) {
	info := doctor.Inspect()
	c.JSON(http.StatusOK, gin.H{
		"environment": info,
		"report":      info.Report(),
	})
}

// handleValidate 返回配置中不合法的任务清单
func (s *Server) handleValidate(c *gin.Context) {
	problems := s.configManager.ValidateJobs()
	c.JSON(http.StatusOK, gin.H{"issues": problems, "count": len(problems)})
}
