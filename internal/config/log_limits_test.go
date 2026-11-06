package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadWithApp 以 map 方式落盘配置，便于精确控制「字段缺失」与「非法值」，
// 避免 json.Marshal 的 omitempty / 零值行为把缺失字段补回来。
func loadWithApp(t *testing.T, app map[string]interface{}) (*ConfigManager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crontab.json")
	raw := map[string]interface{}{"app": app, "jobs": []interface{}{}}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	return cm, path
}

// 默认配置、隐式初始化出来的文件、以及内存中的 AppConfig 必须一致
func TestDefaultLogLimits(t *testing.T) {
	def := DefaultConfig()
	if def.App.LogMaxEntries != 500 || def.App.LogMaxFileMB != 10 {
		t.Fatalf("默认日志上限变化: entries=%d fileMB=%d", def.App.LogMaxEntries, def.App.LogMaxFileMB)
	}

	cm, path := newTestManager(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file CrontabFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.App.LogMaxEntries != 500 || file.App.LogMaxFileMB != 10 {
		t.Errorf("隐式初始化的配置文件缺少日志上限: %+v", file.App)
	}
	got := cm.GetAppConfig()
	if got.LogMaxEntries != 500 || got.LogMaxFileMB != 10 {
		t.Errorf("GetAppConfig 未返回默认日志上限: %+v", got)
	}
}

// 旧版 crontab.json 没有这两个字段时，加载必须补默认值而不是留 0
func TestLoadFillsMissingLogLimits(t *testing.T) {
	cm, _ := loadWithApp(t, map[string]interface{}{"web_port": 8080})
	got := cm.GetAppConfig()
	if got.LogMaxEntries != 500 || got.LogMaxFileMB != 10 {
		t.Errorf("缺失字段未补默认值: %+v", got)
	}

	// 显式写 0 也等同「未配置」，不能让日志上限变成 0
	cm2, _ := loadWithApp(t, map[string]interface{}{
		"log_max_entries": 0,
		"log_max_file_mb": 0,
	})
	got2 := cm2.GetAppConfig()
	if got2.LogMaxEntries != 500 || got2.LogMaxFileMB != 10 {
		t.Errorf("显式 0 应回退默认值: %+v", got2)
	}
	if issues := strings.Join(cm2.ValidateJobs(), "\n"); strings.Contains(issues, "log_max_") {
		t.Errorf("字段为 0 属于「未配置」，不应报校验错误: %s", issues)
	}
}

// 越界值必须回退到默认值，同时通过 validate 明确告知用户
func TestLoadRejectsOutOfRangeLogLimits(t *testing.T) {
	cases := []struct {
		name        string
		entries     int
		fileMB      int
		wantEntries int
		wantFileMB  int
		wantIssue   string
	}{
		{"entries 小于下限", 9, 5, 500, 5, "log_max_entries"},
		{"entries 超过上限", 100001, 5, 500, 5, "log_max_entries"},
		{"entries 为负", -1, 5, 500, 5, "log_max_entries"},
		{"fileMB 超过上限", 200, 2048, 200, 10, "log_max_file_mb"},
		{"fileMB 为负", 200, -1, 200, 10, "log_max_file_mb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm, _ := loadWithApp(t, map[string]interface{}{
				"log_max_entries": tc.entries,
				"log_max_file_mb": tc.fileMB,
			})
			got := cm.GetAppConfig()
			if got.LogMaxEntries != tc.wantEntries || got.LogMaxFileMB != tc.wantFileMB {
				t.Errorf("回退结果不符: got entries=%d fileMB=%d, want %d/%d",
					got.LogMaxEntries, got.LogMaxFileMB, tc.wantEntries, tc.wantFileMB)
			}
			if issues := strings.Join(cm.ValidateJobs(), "\n"); !strings.Contains(issues, tc.wantIssue) {
				t.Errorf("validate 未提示越界字段 %q: %s", tc.wantIssue, issues)
			}
		})
	}
}

// GetAppConfig 返回深拷贝：Web 并发读渠道 + 热加载写渠道不能互相踩踏
func TestGetAppConfigReturnsDeepCopyOfChannels(t *testing.T) {
	cm, _ := loadWithApp(t, map[string]interface{}{
		"notify_channels": []map[string]interface{}{
			{"name": "ops", "type": "http", "urls": []string{"http://127.0.0.1:9/hook"}},
		},
	})

	got := cm.GetAppConfig()
	if len(got.NotifyChannels) != 1 {
		t.Fatalf("应有 1 个渠道，实际 %d", len(got.NotifyChannels))
	}
	got.NotifyChannels[0].Token = "tampered"
	got.NotifyChannels[0].URLs[0] = "http://evil.example/hook"
	got.NotifyChannels = append(got.NotifyChannels, NotifyChannel{Name: "extra"})

	again := cm.GetAppConfig()
	if len(again.NotifyChannels) != 1 {
		t.Errorf("调用方 append 污染了内部 slice: %d 个渠道", len(again.NotifyChannels))
	}
	if again.NotifyChannels[0].Token != "" {
		t.Errorf("调用方修改标量污染了内部渠道: %q", again.NotifyChannels[0].Token)
	}
	if again.NotifyChannels[0].URLs[0] != "http://127.0.0.1:9/hook" {
		t.Errorf("调用方修改 slice 元素污染了内部渠道: %q", again.NotifyChannels[0].URLs[0])
	}
}
