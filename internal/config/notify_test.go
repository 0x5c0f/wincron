package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandSecret(t *testing.T) {
	t.Setenv("WINCRON_TEST_TOKEN", "abc-123")
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"plain", "plain", false},
		{"${ENV:WINCRON_TEST_TOKEN}", "abc-123", false},
		{"key=${ENV:WINCRON_TEST_TOKEN}&x=1", "key=abc-123&x=1", false},
		{"${ENV:WINCRON_MISSING_VAR}", "", true},
		{"${ENV:WINCRON_TEST_TOKEN}${ENV:WINCRON_MISSING_VAR}", "", true},
		{"${BAD:FORMAT}", "${BAD:FORMAT}", false},
	}
	for _, c := range cases {
		got, err := ExpandSecret(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ExpandSecret(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ExpandSecret(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateChannels(t *testing.T) {
	good := NotifyChannel{Name: "ops", Type: "wecom", URL: "https://example.com/hook"}
	if p := ValidateChannels([]NotifyChannel{good}); len(p) != 0 {
		t.Fatalf("合法渠道被报错: %v", p)
	}

	bad := []NotifyChannel{
		{Name: "", Type: "http"},
		{Name: "dup", Type: "http", URLs: []string{"https://a.com"}},
		{Name: "dup", Type: "http", URLs: []string{"https://a.com"}},
		{Name: "bad type", Type: "nope"},
		{Name: "ftp", Type: "http", URLs: []string{"ftp://a.com"}},
		{Name: "no url", Type: "http"},
		{Name: "dd", Type: "dingding", Token: "t"},
		{Name: "tg", Type: "telegram", Token: "t"},
		{Name: "mail", Type: "mail", SMTPHost: "h:25"},
		{Name: "neg", Type: "wecom", URL: "https://a.com", Timeout: -1},
	}
	problems := ValidateChannels(bad)
	for _, want := range []string{"缺少 name", "非法字符", "重复", "不支持的 type", "只允许 http/https", "缺少 urls", "缺少 token 或 secret", "缺少 token 或 chat_ids", "缺少 smtp_host", "不能为负数"} {
		found := false
		for _, p := range problems {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("未检出问题 %q，实际: %v", want, problems)
		}
	}
}

// 校验阶段不能要求 ${ENV:NAME} 已经存在：validate 常在交互式终端执行，
// 而服务可能由 SCM 注入了另一套系统环境变量，误报会让人以为配置写错了。
func TestValidateChannelsAllowsEnvPlaceholders(t *testing.T) {
	if err := os.Unsetenv("WINCRON_UNSET_KEY"); err != nil {
		t.Fatal(err)
	}
	channels := []NotifyChannel{
		{Name: "wecom", Type: "wecom", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=${ENV:WINCRON_UNSET_KEY}"},
		{Name: "slack", Type: "slack", Token: "${ENV:WINCRON_UNSET_KEY}", Receivers: []string{"#ops"}},
		{Name: "http", Type: "http", URLs: []string{"https://${ENV:WINCRON_UNSET_KEY}/hook"}},
	}
	if problems := ValidateChannels(channels); len(problems) != 0 {
		t.Fatalf("环境变量占位符不应在校验阶段报错: %v", problems)
	}
}

func TestValidateJobChannels(t *testing.T) {
	channels := []NotifyChannel{{Name: "ops", Type: "http", URLs: []string{"https://a.com"}}}
	jobs := []JobConfig{
		{ID: "a", Notify: NotifyConfig{Channels: []string{"ops"}}},
		{ID: "b", Notify: NotifyConfig{Channels: []string{"missing"}}},
		{ID: "c", Notify: NotifyConfig{Channels: []string{"ops", "ops"}}},
	}
	problems := ValidateJobChannels(jobs, channels)
	if len(problems) != 2 {
		t.Fatalf("期望 2 个问题，实际 %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "不存在") || !strings.Contains(problems[1], "重复") {
		t.Errorf("问题内容不符: %v", problems)
	}
}

func TestCloneChannelsDeepCopy(t *testing.T) {
	ch := NotifyChannel{Name: "ops", Type: "http", URLs: []string{"https://a.com"}, ChatIDs: []int64{1}}
	cl := ch.Clone()
	cl.URLs[0] = "MUTATED"
	cl.ChatIDs[0] = 999
	if ch.URLs[0] == "MUTATED" || ch.ChatIDs[0] == 999 {
		t.Fatal("Clone() 暴露了内部 slice")
	}
	list := cloneChannels([]NotifyChannel{ch})
	list[0].URLs[0] = "MUTATED2"
	if ch.URLs[0] == "MUTATED2" {
		t.Fatal("cloneChannels() 暴露了内部 slice")
	}
}

const legacyConfigJSON = `{
  "app": {
    "service_name": "WinCron",
    "web_username": "admin",
    "web_password": "secret",
    "smtp_host": "smtp.example.com",
    "smtp_port": "587",
    "smtp_user": "from@example.com",
    "smtp_pass": "pw"
  },
  "jobs": [
    {"id":"job-1","name":"a","cron":"* * * * *","command":"echo a",
     "notify":{"on_failure":true,"webhooks":["https://hook.example.com/a"],"emails":["ops@example.com"]}},
    {"id":"job-2","name":"b","cron":"* * * * *","command":"echo b",
     "notify":{"on_failure":true,"emails":["dev@example.com"]}}
  ]
}`

func TestMigrateLegacyNotify(t *testing.T) {
	var cfg CrontabFile
	if err := json.Unmarshal([]byte(legacyConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateLegacyNotify(&cfg, []byte(legacyConfigJSON))
	if err != nil || !migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if len(cfg.App.NotifyChannels) != 3 {
		t.Fatalf("期望 3 个渠道，实际 %d: %+v", len(cfg.App.NotifyChannels), cfg.App.NotifyChannels)
	}
	byName := map[string]NotifyChannel{}
	for _, ch := range cfg.App.NotifyChannels {
		byName[ch.Name] = ch
	}
	httpCh := byName["legacy-job-1-http"]
	if httpCh.Type != "http" || len(httpCh.URLs) != 1 || httpCh.URLs[0] != "https://hook.example.com/a" {
		t.Errorf("http 渠道迁移错误: %+v", httpCh)
	}
	mailCh := byName["legacy-job-1-mail"]
	if mailCh.Type != "mail" || mailCh.SMTPHost != "smtp.example.com:587" ||
		mailCh.From != "from@example.com" || mailCh.Password != "pw" ||
		len(mailCh.Receivers) != 1 {
		t.Errorf("mail 渠道迁移错误: %+v", mailCh)
	}
	if len(cfg.Jobs[0].Notify.Channels) != 2 || len(cfg.Jobs[1].Notify.Channels) != 1 {
		t.Errorf("任务渠道引用错误: %+v", cfg.Jobs)
	}
	if !cfg.Jobs[0].Notify.OnFailure {
		t.Error("迁移丢失了 on_failure")
	}

	// 已迁移过的配置不应再次迁移。
	newRaw, _ := json.Marshal(cfg)
	again, err := MigrateLegacyNotify(&cfg, newRaw)
	if err != nil || again {
		t.Errorf("重复迁移: again=%v err=%v", again, err)
	}
}

func TestLoadMigratesAndWritesBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crontab.json")
	if err := os.WriteFile(path, []byte(legacyConfigJSON), 0644); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	bak, err := os.ReadFile(path + ".pre-notify.bak")
	if err != nil {
		t.Fatalf("未生成升级前备份: %v", err)
	}
	if string(bak) != legacyConfigJSON {
		t.Error("备份内容与原始文件不一致")
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), `"webhooks"`) || strings.Contains(string(written), `"smtp_pass"`) {
		t.Error("写回后仍残留旧字段")
	}
	var roundtrip CrontabFile
	if err := json.Unmarshal(written, &roundtrip); err != nil {
		t.Fatalf("写回的不是合法配置: %v", err)
	}
	if len(roundtrip.App.NotifyChannels) != 3 {
		t.Errorf("写回后渠道数 %d", len(roundtrip.App.NotifyChannels))
	}

	// 二次加载不应再追加渠道，也不应覆盖第一次的备份。
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	if got := len(cm.Get().App.NotifyChannels); got != 3 {
		t.Errorf("二次加载渠道数变成 %d", got)
	}
}
