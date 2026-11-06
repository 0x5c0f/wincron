package web

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"wincron/internal/config"
)

// handleNotifyChannels 返回全部通知渠道及其就绪状态。
// 只回传名称、类型与是否可用，绝不回传 token / secret / 密码等凭据。
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
	channels := s.configManager.GetAppConfig().NotifyChannels
	definitions := make([]notifyChannelDefinition, 0, len(channels))
	for _, ch := range channels {
		definitions = append(definitions, notifyChannelDefinition{
			Name:     ch.Name,
			Type:     ch.Type,
			Required: config.ChannelRequiredFields(ch.Type),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"channels":  definitions,
		"statuses":  statuses,
		"supported": config.SupportedChannelTypes,
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
