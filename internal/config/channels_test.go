package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func managerWithChannels(t *testing.T, channels []NotifyChannel, jobs []JobConfig) (*ConfigManager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crontab.json")
	cfg := DefaultConfig()
	cfg.App.NotifyChannels = channels
	cfg.Jobs = jobs
	data, err := json.MarshalIndent(cfg, "", "  ")
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

func readNotifyChannels(t *testing.T, path string) []NotifyChannel {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file CrontabFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file.App.NotifyChannels
}

func httpChannel(name string) NotifyChannel {
	return NotifyChannel{
		Name: name,
		Type: "http",
		URLs: []string{"http://127.0.0.1:9/hook"},
	}
}

// Web 新增 / 编辑渠道必须立刻写回 crontab.json，重启后仍然在
func TestUpsertNotifyChannelAddUpdateAndPersist(t *testing.T) {
	cm, path := newTestManager(t)
	ch := httpChannel("ops-http")

	if err := cm.UpsertNotifyChannel(ch, ""); err != nil {
		t.Fatalf("新增渠道失败: %v", err)
	}
	all := cm.GetNotifyChannels()
	if len(all) != 1 || all[0].Name != "ops-http" || all[0].URLs[0] != "http://127.0.0.1:9/hook" {
		t.Fatalf("内存渠道不正确: %+v", all)
	}
	if got := readNotifyChannels(t, path); len(got) != 1 || got[0].Name != "ops-http" {
		t.Fatalf("新增未落盘: %+v", got)
	}

	ch.URLs = []string{"http://127.0.0.1:10/hook2"}
	ch.Timeout = 7
	if err := cm.UpsertNotifyChannel(ch, "ops-http"); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}
	all = cm.GetNotifyChannels()
	if len(all) != 1 {
		t.Fatalf("更新变成了追加，现有 %d 个渠道", len(all))
	}
	if all[0].URLs[0] != "http://127.0.0.1:10/hook2" || all[0].Timeout != 7 {
		t.Errorf("更新内容不正确: %+v", all[0])
	}

	cm2 := NewConfigManager(path)
	if err := cm2.Load(); err != nil {
		t.Fatal(err)
	}
	again := cm2.GetNotifyChannels()
	if len(again) != 1 || again[0].URLs[0] != "http://127.0.0.1:10/hook2" || again[0].Timeout != 7 {
		t.Errorf("重新加载后更新丢失: %+v", again)
	}
}

// 改名、引用不存在的渠道、重复名称、非法定义都必须被拒绝，且不能写脏配置
func TestUpsertNotifyChannelRejectsInvalidChanges(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.UpsertNotifyChannel(httpChannel("ops-http"), ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		ch       NotifyChannel
		original string
		want     string
	}{
		{"名称不可修改", httpChannel("renamed"), "ops-http", "渠道名称不可修改"},
		{"原渠道不存在", httpChannel("ghost"), "ghost", "不存在"},
		{"名称重复", httpChannel("ops-http"), "", "重复"},
		{"缺少必填 urls", NotifyChannel{Name: "bad", Type: "http"}, "", "缺少 urls"},
		{"URL 协议非法", NotifyChannel{Name: "bad", Type: "http", URLs: []string{"ftp://x/hook"}}, "", "只允许 http/https"},
		{"类型不支持", NotifyChannel{Name: "bad", Type: "carrier-pigeon"}, "", "不支持的 type"},
		{"名称含非法字符", httpChannel("bad name/"), "", "非法字符"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := cm.UpsertNotifyChannel(tc.ch, tc.original); err == nil {
				t.Fatalf("应被拒绝，但没有报错")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息不符，期望含 %q，实际 %q", tc.want, err.Error())
			}
		})
	}

	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("校验失败却改动了配置文件")
	}
	if got := cm.GetNotifyChannels(); len(got) != 1 || got[0].Name != "ops-http" {
		t.Errorf("校验失败污染了内存配置: %+v", got)
	}
}

// 名称是任务的引用键，改名会静默丢通知，删除被引用渠道同样要拦住
func TestDeleteNotifyChannelRejectsWhenReferenced(t *testing.T) {
	ch := httpChannel("ops-http")
	job := validJob("job-ref")
	job.Notify = NotifyConfig{Channels: []string{"ops-http"}}
	cm, path := managerWithChannels(t, []NotifyChannel{ch}, []JobConfig{job})

	if err := cm.DeleteNotifyChannel("nope"); err == nil {
		t.Error("删除不存在的渠道应报错")
	}
	err := cm.DeleteNotifyChannel("ops-http")
	if err == nil {
		t.Fatal("删除被任务引用的渠道应被拒绝")
	}
	if !strings.Contains(err.Error(), "job-ref") {
		t.Errorf("错误信息应指出引用方: %q", err.Error())
	}
	if got := readNotifyChannels(t, path); len(got) != 1 {
		t.Errorf("被拒绝的删除不应改动文件: %+v", got)
	}

	// 解除引用后可以正常删除，且写回文件
	cfg := cm.Get()
	cfg.Jobs[0].Notify.Channels = nil
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	if err := cm.DeleteNotifyChannel("ops-http"); err != nil {
		t.Fatalf("解除引用后删除失败: %v", err)
	}
	if got := cm.GetNotifyChannels(); len(got) != 0 {
		t.Errorf("删除后内存仍有渠道: %+v", got)
	}
	if got := readNotifyChannels(t, path); len(got) != 0 {
		t.Errorf("删除未写回文件: %+v", got)
	}
}

// 单渠道详情也要深拷贝，编辑弹窗里的临时改动不能影响运行中的配置
func TestGetNotifyChannelReturnsDeepCopy(t *testing.T) {
	cm, _ := managerWithChannels(t, []NotifyChannel{httpChannel("ops-http")}, nil)
	ch, ok := cm.GetNotifyChannel("ops-http")
	if !ok {
		t.Fatal("按名称未找到渠道")
	}
	ch.URLs[0] = "http://evil.example/hook"
	ch.Token = "tampered"

	again, ok := cm.GetNotifyChannel("ops-http")
	if !ok {
		t.Fatal("渠道意外消失")
	}
	if again.URLs[0] != "http://127.0.0.1:9/hook" || again.Token != "" {
		t.Errorf("详情返回值污染了内部渠道: %+v", again)
	}
	if _, ok := cm.GetNotifyChannel("missing"); ok {
		t.Error("不存在的渠道不应返回 ok")
	}
}

// Web 的「发送超时」设置必须限制范围并持久化
func TestSetNotifyTimeoutRangeAndPersist(t *testing.T) {
	cm, path := newTestManager(t)
	for _, bad := range []int{0, -1, 301, 99999} {
		if err := cm.SetNotifyTimeout(bad); err == nil {
			t.Errorf("超时 %d 应被拒绝", bad)
		}
	}
	if got := cm.GetAppConfig().NotifyTimeout; got != 10 {
		t.Errorf("非法值不应改动当前超时，实际 %d", got)
	}

	if err := cm.SetNotifyTimeout(30); err != nil {
		t.Fatalf("合法超时保存失败: %v", err)
	}
	if got := cm.GetAppConfig().NotifyTimeout; got != 30 {
		t.Errorf("内存超时未更新: %d", got)
	}

	data, _ := os.ReadFile(path)
	var file CrontabFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.App.NotifyTimeout != 30 {
		t.Errorf("超时未写回配置文件: %d", file.App.NotifyTimeout)
	}
}
