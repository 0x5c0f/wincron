package config

import (
	"fmt"
	"strings"
)

// GetNotifyChannels 返回全部通知渠道的深拷贝，供 Web 列表使用。
func (cm *ConfigManager) GetNotifyChannels() []NotifyChannel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cloneChannels(cm.config.App.NotifyChannels)
}

// GetNotifyChannel 按名称返回单个渠道的深拷贝。
// 这个接口只在已登录的管理会话里调用，用于编辑时回填完整定义（含凭据）。
func (cm *ConfigManager) GetNotifyChannel(name string) (NotifyChannel, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for _, ch := range cm.config.App.NotifyChannels {
		if ch.Name == name {
			return ch.Clone(), true
		}
	}
	return NotifyChannel{}, false
}

func channelProblems(candidate []NotifyChannel) error {
	problems := ValidateChannels(candidate)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "；"))
}

// UpsertNotifyChannel 新增或整体替换一个渠道，并原子写回 crontab.json。
//
// originalName 为空表示新增；非空表示按该名称替换。渠道名称不允许修改，
// 因为任务只按名称引用渠道，改名会让所有引用静默失效。
// 与直接编辑配置文件一样，落盘前会跑完整的渠道合法性校验。
func (cm *ConfigManager) UpsertNotifyChannel(ch NotifyChannel, originalName string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	channels := cloneChannels(cm.config.App.NotifyChannels)
	if originalName == "" {
		channels = append(channels, ch.Clone())
	} else {
		if ch.Name != originalName {
			return fmt.Errorf("渠道名称不可修改；如确需改名，请新增渠道后调整任务的通知渠道引用")
		}
		found := false
		for i := range channels {
			if channels[i].Name == originalName {
				channels[i] = ch.Clone()
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("通知渠道 %q 不存在", originalName)
		}
	}

	if err := channelProblems(channels); err != nil {
		return err
	}
	cm.config.App.NotifyChannels = channels
	return cm.saveLocked()
}

// DeleteNotifyChannel 删除渠道。被任务引用时拒绝删除，避免任务通知静默丢失。
func (cm *ConfigManager) DeleteNotifyChannel(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	idx := -1
	for i, ch := range cm.config.App.NotifyChannels {
		if ch.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("通知渠道 %q 不存在", name)
	}

	var refs []string
	for _, job := range cm.config.Jobs {
		for _, ref := range job.Notify.Channels {
			if ref == name {
				refs = append(refs, job.ID)
				break
			}
		}
	}
	if len(refs) > 0 {
		return fmt.Errorf("通知渠道 %q 正被 %d 个任务引用（%s），请先修改这些任务的通知渠道",
			name, len(refs), strings.Join(refs, ", "))
	}

	channels := cloneChannels(cm.config.App.NotifyChannels)
	cm.config.App.NotifyChannels = append(channels[:idx], channels[idx+1:]...)
	return cm.saveLocked()
}

// SetNotifyTimeout 修改单渠道通知发送超时（秒），并原子写回配置文件。
func (cm *ConfigManager) SetNotifyTimeout(seconds int) error {
	if seconds < 1 || seconds > 300 {
		return fmt.Errorf("通知超时须在 1-300 秒之间，当前为 %d", seconds)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.config.App.NotifyTimeout = seconds
	return cm.saveLocked()
}
