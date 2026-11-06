package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T) (*ConfigManager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crontab.json")
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("初始化配置失败: %v", err)
	}
	return cm, path
}

func validJob(id string) JobConfig {
	return JobConfig{
		ID:       id,
		Name:     "test_write",
		Cron:     "* * * * *",
		Command:  `echo aaa >> c:\b.txt`,
		Enabled:  true,
		Notify:   NotifyConfig{Channels: []string{}},
		Env:      map[string]string{},
		WorkDir:  "",
		Timeout:  0,
		MaxRetry: 0,
	}
}

// 5 个 * 的 cron 和含空格/重定向的命令必须原样落盘
func TestAddJobPreservesSpacedCronAndCommand(t *testing.T) {
	cm, path := newTestManager(t)
	job := validJob("job-3")
	if err := cm.AddJob(job); err != nil {
		t.Fatalf("AddJob 失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file CrontabFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("写出的配置不是合法 JSON: %v", err)
	}
	if len(file.Jobs) != 1 {
		t.Fatalf("应有 1 个任务，实际 %d", len(file.Jobs))
	}
	if file.Jobs[0].Cron != "* * * * *" {
		t.Errorf("cron 被改写: %q", file.Jobs[0].Cron)
	}
	if file.Jobs[0].Command != `echo aaa >> c:\b.txt` {
		t.Errorf("命令被改写: %q", file.Jobs[0].Command)
	}
	if file.Jobs[0].WorkDir != "" {
		t.Errorf("work_dir 不应有残留值: %q", file.Jobs[0].WorkDir)
	}

	got, err := cm.GetJob("job-3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cron != "* * * * *" || got.Command != `echo aaa >> c:\b.txt` {
		t.Errorf("读回内存配置不一致: %+v", got)
	}
}

// 被截断成单个 "*" 的 cron 必须被拒绝，且不能写脏数据
func TestAddJobRejectsTruncatedCronAndDoesNotTouchFile(t *testing.T) {
	cm, path := newTestManager(t)
	before, _ := os.ReadFile(path)

	job := validJob("bad")
	job.Cron = "*"
	if err := cm.AddJob(job); err == nil {
		t.Fatal("单字段 cron 应当被拒绝")
	}

	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("校验失败却改动了配置文件")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("校验失败不应留下临时文件")
	}
	if len(cm.GetJobs()) != 0 {
		t.Error("校验失败不应把任务加进内存")
	}
}

func TestUpdateJobRejectsBadCron(t *testing.T) {
	cm, _ := newTestManager(t)
	if err := cm.AddJob(validJob("job-1")); err != nil {
		t.Fatal(err)
	}
	job, _ := cm.GetJob("job-1")
	job.Cron = "* * * *" // 4 字段
	if err := cm.UpdateJob(*job); err == nil {
		t.Fatal("UpdateJob 应当拒绝 4 字段 cron")
	}
	if got, _ := cm.GetJob("job-1"); got.Cron != "* * * * *" {
		t.Errorf("失败更新不应污染原任务: %q", got.Cron)
	}
}

func TestAddJobRejectsEmptyCommandAndDuplicateID(t *testing.T) {
	cm, _ := newTestManager(t)
	empty := validJob("j1")
	empty.Command = "   "
	if err := cm.AddJob(empty); err == nil {
		t.Error("空命令应当被拒绝")
	}
	if err := cm.AddJob(validJob("j1")); err != nil {
		t.Fatal(err)
	}
	if err := cm.AddJob(validJob("j1")); err == nil {
		t.Error("重复 ID 应当被拒绝")
	}
}

func TestGetReturnsDeepCopy(t *testing.T) {
	cm, _ := newTestManager(t)
	job := validJob("job-1")
	job.Notify.Channels = []string{"ops"}
	if err := cm.AddJob(job); err != nil {
		t.Fatal(err)
	}

	cfg := cm.Get()
	cfg.Jobs[0].Cron = "MUTATED"
	cfg.Jobs[0].Env["X"] = "Y"
	cfg.Jobs[0].Notify.Channels[0] = "MUTATED"
	cfg.App.WebPort = 1

	again := cm.Get()
	if again.Jobs[0].Cron == "MUTATED" {
		t.Error("Get() 暴露了内部 Job 结构体")
	}
	if _, ok := again.Jobs[0].Env["X"]; ok {
		t.Error("Get() 暴露了内部 Env map")
	}
	if again.Jobs[0].Notify.Channels[0] == "MUTATED" {
		t.Error("Get() 暴露了内部 Channels slice")
	}
	if again.App.WebPort == 1 {
		t.Error("Get() 暴露了内部 App 配置")
	}
}

func TestGetJobsReturnsDeepCopy(t *testing.T) {
	cm, _ := newTestManager(t)
	if err := cm.AddJob(validJob("job-1")); err != nil {
		t.Fatal(err)
	}
	jobs := cm.GetJobs()
	jobs[0].Cron = "MUTATED"
	jobs[0].Env["X"] = "Y"
	if again := cm.GetJobs(); again[0].Cron == "MUTATED" || again[0].Env["X"] == "Y" {
		t.Error("GetJobs() 返回了内部引用")
	}
}

func TestValidateJobsReportsLegacyBadData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.json")
	// 模拟用户手动改过的、带旧版 CLI 脏数据的配置
	raw := `{"app":{"service_name":"WinCron"},"jobs":[{"id":"job-3","name":"test_write","cron":"*","command":"echo aaa >> c:\\b.txt","work_dir":"*","enabled":true}]}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("历史配置不应阻塞加载: %v", err)
	}
	problems := cm.ValidateJobs()
	if len(problems) != 1 {
		t.Fatalf("应报告 1 个问题，实际 %d: %v", len(problems), problems)
	}
}

func TestAtomicSaveNoTempLeftover(t *testing.T) {
	cm, path := newTestManager(t)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := cm.AddJob(validJob(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("原子写入后不应残留 .tmp 文件")
	}
	data, _ := os.ReadFile(path)
	var f CrontabFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("连续写入后配置损坏: %v", err)
	}
	if len(f.Jobs) != 5 {
		t.Errorf("应有 5 个任务，实际 %d", len(f.Jobs))
	}
}

// 手工编辑配置文件的边界场景：编码、任务 ID、应用级配置问题

func TestLoadAcceptsUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.json")
	raw := `{"app":{"service_name":"BOM测试"},"jobs":[{"id":"job-1","name":"中文","cron":"* * * * *","command":"echo ok","enabled":true}]}`
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte(raw)...), 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("记事本另存的 UTF-8 BOM 文件应能加载: %v", err)
	}
	jobs := cm.GetJobs()
	if len(jobs) != 1 || jobs[0].Name != "中文" {
		t.Errorf("BOM 配置内容解析不正确: %+v", jobs)
	}
	if problems := cm.ValidateJobs(); len(problems) != 0 {
		t.Errorf("合法配置不应报告问题: %v", problems)
	}
}

func TestLoadAcceptsGBKConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.json")
	// 用 GBK 字节写入中文任务名，模拟记事本 ANSI 保存
	gbkName := []byte{0xD6, 0xD0, 0xCE, 0xC4} // "中文"
	raw := append([]byte(`{"jobs":[{"id":"job-1","name":"`), gbkName...)
	raw = append(raw, []byte(`","cron":"* * * * *","command":"echo ok","enabled":true}]}`)...)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("GBK 配置应能自动转码加载: %v", err)
	}
	if got := cm.GetJobs()[0].Name; got != "中文" {
		t.Errorf("GBK 中文未正确转为 UTF-8: %q", got)
	}
}

func TestValidateJobID(t *testing.T) {
	for _, bad := range []string{"", "  ", "../evil", `a\b`, "a/b", "a:b", "a*b",
		"..", "a..b", " id", "id ", "a\tb"} {
		if err := validateJobID(bad); err == nil {
			t.Errorf("非法 ID %q 应当被拒绝", bad)
		}
	}
	for _, good := range []string{"job-1", "job_2", "backup.daily", "中文任务", "job-123"} {
		if err := validateJobID(good); err != nil {
			t.Errorf("合法 ID %q 被误拒: %v", good, err)
		}
	}
}

func TestValidateJobsReportsDuplicateAndTraversalIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.json")
	raw := `{"jobs":[
		{"id":"dup","name":"a","cron":"* * * * *","command":"echo a","enabled":true},
		{"id":"dup","name":"b","cron":"* * * * *","command":"echo b","enabled":true},
		{"id":"../evil","name":"c","cron":"* * * * *","command":"echo c","enabled":true}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	problems := cm.ValidateJobs()
	joined := strings.Join(problems, " | ")
	if !strings.Contains(joined, "重复") {
		t.Errorf("未报告重复 ID: %v", problems)
	}
	if !strings.Contains(joined, "../evil") {
		t.Errorf("未报告路径穿越 ID: %v", problems)
	}
}

func TestAppConfigIssuesAreNormalizedAndReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.json")
	raw := `{"app":{"web_port":70000,"web_username":"admin","web_password":""},"jobs":[]}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	if got := cm.GetAppConfig().WebPort; got != 9090 {
		t.Errorf("越界端口应回退到 9090，实际 %d", got)
	}
	joined := strings.Join(cm.ValidateJobs(), " | ")
	if !strings.Contains(joined, "Web端口") || !strings.Contains(joined, "web_password") {
		t.Errorf("应用级配置问题未上报: %v", cm.ValidateJobs())
	}
}

func TestCheckReloadFiresCallbackOnce(t *testing.T) {
	cm, path := newTestManager(t)
	if cm.CheckReload() {
		t.Error("刚加载完不应触发重载")
	}

	called := 0
	cm.SetOnReload(func(_ *CrontabFile) { called++ })

	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	if !cm.CheckReload() {
		t.Fatal("文件变更后应触发重载")
	}
	if called != 1 {
		t.Errorf("回调应触发 1 次，实际 %d", called)
	}
	if cm.CheckReload() {
		t.Error("重复检查不应再次触发重载")
	}
}
