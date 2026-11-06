package web

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"wincron/internal/config"
)

// handleNotifyChannels 返回全部通知渠道及其就绪状态。
// 只回传名称、类型与是否可用，绝不回传 token / secret / 密码等凭据。
// 编辑用的完整定义在单独的 GET /notify/channels/:name 里按名称获取，
// 这样「看一眼列表」不会把凭据带进页面 DOM 或浏览器缓存。
func (s *Server) handleNotifyChannels(c *gin.Context) {
	statuses := []notifyChannelView{}
	if s.notifier != nil {
		for _, st := range s.notifier.Statuses() {
			statuses = append(statuses, notifyChannelView{
				Name:    st.Name,
				Type:    st.Type,
				Ready:   st.Ready,
				Problem: st.Problem,
			})
		}
	}
	app := s.configManager.GetAppConfig()
	definitions := make([]notifyChannelDefinition, 0, len(app.NotifyChannels))
	for _, ch := range app.NotifyChannels {
		definitions = append(definitions, notifyChannelDefinition{
			Name:     ch.Name,
			Type:     ch.Type,
			Required: config.ChannelRequiredFields(ch.Type),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"channels":       definitions,
		"statuses":       statuses,
		"supported":      config.SupportedChannelTypes,
		"notify_timeout": app.NotifyTimeout,
		// 前端按类型动态渲染必填字段，避免把必填规则在前后端各维护一份
		"required_map": func() map[string][]string {
			m := make(map[string][]string, len(config.SupportedChannelTypes))
			for _, t := range config.SupportedChannelTypes {
				m[t] = config.ChannelRequiredFields(t)
			}
			return m
		}(),
		"build_errors": func() []string {
			if s.notifier == nil {
				return nil
			}
			return s.notifier.BuildErrors()
		}(),
	})
}

type notifyChannelView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
}

type notifyChannelDefinition struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required []string `json:"required"`
}

// handleGetNotifyChannel 返回单个渠道的完整定义（含凭据），仅供已登录的
// 管理会话在编辑弹窗里回填。列表接口仍然不回传任何凭据。
func (s *Server) handleGetNotifyChannel(c *gin.Context) {
	ch, ok := s.configManager.GetNotifyChannel(c.Param("name"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知渠道不存在"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"channel": ch})
}

// handleAddNotifyChannel 新增渠道。校验与写回都在 ConfigManager 内完成，
// 与直接编辑 crontab.json 走的是同一套 ValidateChannels 规则。
func (s *Server) handleAddNotifyChannel(c *gin.Context) {
	var ch config.NotifyChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.configManager.UpsertNotifyChannel(ch, ""); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.reloadNotifier()
	c.JSON(http.StatusOK, gin.H{"message": "通知渠道已添加", "name": ch.Name, "type": ch.Type})
}

// handleUpdateNotifyChannel 整体替换一个渠道。名称不允许修改，否则所有
// 引用它的任务都会静默失去通知。
func (s *Server) handleUpdateNotifyChannel(c *gin.Context) {
	name := c.Param("name")
	var ch config.NotifyChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if ch.Name == "" {
		ch.Name = name
	}
	if ch.Name != name {
		c.JSON(http.StatusBadRequest, gin.H{"error": "渠道名称不可修改；请新增渠道后调整任务的通知渠道引用"})
		return
	}
	if err := s.configManager.UpsertNotifyChannel(ch, name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.reloadNotifier()
	c.JSON(http.StatusOK, gin.H{"message": "通知渠道已更新", "name": ch.Name, "type": ch.Type})
}

// handleDeleteNotifyChannel 删除渠道；被任务引用时由 ConfigManager 拒绝。
func (s *Server) handleDeleteNotifyChannel(c *gin.Context) {
	name := c.Param("name")
	if err := s.configManager.DeleteNotifyChannel(name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.reloadNotifier()
	c.JSON(http.StatusOK, gin.H{"message": "通知渠道已删除", "name": name})
}

// handleUpdateNotifySettings 修改全局通知发送超时（app.notify_timeout）。
func (s *Server) handleUpdateNotifySettings(c *gin.Context) {
	var req struct {
		NotifyTimeout int `json:"notify_timeout"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.configManager.SetNotifyTimeout(req.NotifyTimeout); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.reloadNotifier()
	c.JSON(http.StatusOK, gin.H{"message": "通知设置已保存", "notify_timeout": req.NotifyTimeout})
}

// reloadNotifier 让 Web 保存后立刻生效，不用等 10 秒一次的配置热加载。
// 配置写回由 saveLocked 更新了 modTime/size，因此 CheckReload 不会重复触发。
func (s *Server) reloadNotifier() {
	if s.notifier == nil {
		return
	}
	app := s.configManager.GetAppConfig()
	s.notifier.Reload(app.NotifyChannels, app.NotifyTimeout)
}

// handleTestNotifyChannel 立即向指定渠道发一条测试消息。
// 这是判断 token 是否有效、机器人是否被禁言最直接的方式。
func (s *Server) handleTestNotifyChannel(c *gin.Context) {
	if s.notifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "通知器未初始化"})
		return
	}
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "渠道名称不能为空"})
		return
	}
	if err := s.notifier.TestChannel(name); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "测试通知已发送", "channel": name})
}
