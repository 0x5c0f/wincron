package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"wincron/internal/config"
	"wincron/internal/executor"
)

// TestBuildServiceAllOfflineTypes 覆盖所有不需要联网即可构造的渠道类型。
// telegram 的构造函数会联网验证 token，单独在校验路径里覆盖。
func TestBuildServiceAllOfflineTypes(t *testing.T) {
	cases := []config.NotifyChannel{
		{Name: "http", Type: "http", URLs: []string{"https://example.com/hook"}},
		{Name: "wecom", Type: "wecom", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x"},
		{Name: "dingding", Type: "dingding", Token: "t", Secret: "s"},
		{Name: "lark", Type: "lark", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/x"},
		{Name: "slack", Type: "slack", Token: "t", Receivers: []string{"#ops"}},
		{Name: "bark", Type: "bark", Token: "device-key"},
		{Name: "mail", Type: "mail", SMTPHost: "smtp.example.com:587", From: "a@b.com", Receivers: []string{"c@d.com"}},
		{Name: "msteams", Type: "msteams", Receivers: []string{"https://example.com/teams"}},
		{Name: "discord", Type: "discord", Token: "t", Receivers: []string{"123"}},
		{Name: "pushover", Type: "pushover", Token: "t", Receivers: []string{"u1"}},
	}
	for _, ch := range cases {
		svc, err := buildService(ch)
		if err != nil {
			t.Errorf("%s: 构造失败: %v", ch.Type, err)
			continue
		}
		if svc == nil {
			t.Errorf("%s: 返回了 nil service", ch.Type)
		}
	}
}

func TestBuildServiceRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		ch   config.NotifyChannel
		want string
	}{
		{"unknown type", config.NotifyChannel{Name: "x", Type: "carrier-pigeon"}, "不支持的 type"},
		{"http no urls", config.NotifyChannel{Name: "x", Type: "http"}, "至少一个 urls"},
		{"wecom no url", config.NotifyChannel{Name: "x", Type: "wecom"}, "需要 url"},
		{"dingding no secret", config.NotifyChannel{Name: "x", Type: "dingding", Token: "t"}, "token 与 secret"},
		{"telegram missing chat ids", config.NotifyChannel{Name: "x", Type: "telegram", Token: "t"}, "chat_ids"},
		{"missing env", config.NotifyChannel{Name: "x", Type: "slack", Token: "${ENV:WINCRON_ABSENT_TOKEN}", Receivers: []string{"#a"}}, "环境变量未设置"},
	}
	for _, c := range cases {
		_, err := buildService(c.ch)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, 期望包含 %q", c.name, err, c.want)
		}
	}
}

func TestExpandChannelFromEnv(t *testing.T) {
	t.Setenv("WINCRON_TEST_HOOK", "https://example.com/from-env")
	ch := config.NotifyChannel{
		Name: "ops", Type: "http",
		URLs:      []string{"${ENV:WINCRON_TEST_HOOK}"},
		Token:     "${ENV:WINCRON_TEST_HOOK}",
		Receivers: []string{"plain@example.com"},
	}
	got, err := expandChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if got.URLs[0] != "https://example.com/from-env" || got.Token != "https://example.com/from-env" {
		t.Errorf("环境变量展开不完整: %+v", got)
	}
	if ch.URLs[0] != "${ENV:WINCRON_TEST_HOOK}" {
		t.Error("expandChannel 修改了原始配置")
	}
}

// TestHTTPChannelSendsNotifyPayload 用真实 HTTP POST 验证 notify 的默认 schema。
func TestHTTPChannelSendsNotifyPayload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, err := buildService(config.NotifyChannel{Name: "h", Type: "http", URLs: []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Send(ctx, "标题", "正文"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got["subject"] != "标题" || got["message"] != "正文" {
		t.Errorf("payload 不符合 notify 默认 schema: %+v", got)
	}
}

// TestWecomChannelSchema 验证企业微信群机器人专用的 msgtype / text schema，
// 这是「全部按 notify 设计」仍能覆盖企微的关键。
func TestWecomChannelSchema(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, err := buildService(config.NotifyChannel{
		Name: "wecom", Type: "wecom", URL: srv.URL,
		MentionedMobileList: []string{"@all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Send(ctx, "[WinCron] 任务失败", "退出码 1"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got["msgtype"] != "text" {
		t.Errorf("msgtype=%v", got["msgtype"])
	}
	text, ok := got["text"].(map[string]any)
	if !ok {
		t.Fatalf("text 字段类型错误: %+v", got)
	}
	content, _ := text["content"].(string)
	if !strings.Contains(content, "[WinCron] 任务失败") || !strings.Contains(content, "退出码 1") {
		t.Errorf("content 内容错误: %q", content)
	}
	if _, ok := text["mentioned_mobile_list"]; !ok {
		t.Errorf("缺少 @all: %+v", text)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("中文", 2000) // 12000 字节
	got := truncateUTF8(s, 100)
	if !utf8.ValidString(got) {
		t.Error("截断产生了非法 UTF-8")
	}
	if len([]byte(strings.TrimSuffix(got, "\n...(已截断)"))) > 100 {
		t.Error("截断后仍超过字节上限")
	}
	if got := truncateUTF8("short", 100); got != "short" {
		t.Errorf("短文本被误改: %q", got)
	}
}

func testResult(success bool) *executor.ExecResult {
	return &executor.ExecResult{
		JobID: "job-1", JobName: "备份", Command: "echo hi",
		StartTime: time.Now(), EndTime: time.Now(),
		Duration: 0.5, ExitCode: 1, Success: success,
		Output: "hello", Error: "boom",
	}
}

func TestNotifySyncTriggerRules(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := config.NotifyChannel{Name: "ops", Type: "http", URLs: []string{srv.URL}}
	n := NewNotifier([]config.NotifyChannel{ch}, 5, nil)
	job := &config.JobConfig{ID: "job-1", Name: "备份", Notify: config.NotifyConfig{
		OnFailure: true, OnSuccess: false, Channels: []string{"ops"},
	}}

	if errs := n.NotifySync(job, testResult(false)); len(errs) != 0 {
		t.Fatalf("失败通知发送出错: %v", errs)
	}
	if hits != 1 {
		t.Errorf("失败时应发送 1 次，实际 %d", hits)
	}
	if errs := n.NotifySync(job, testResult(true)); len(errs) != 0 {
		t.Fatalf("成功通知不应发送却报错: %v", errs)
	}
	if hits != 1 {
		t.Errorf("on_success=false 时仍发送了通知，hits=%d", hits)
	}

	job.Notify.OnSuccess = true
	if errs := n.NotifySync(job, testResult(true)); len(errs) != 0 {
		t.Fatalf("成功通知发送出错: %v", errs)
	}
	if hits != 2 {
		t.Errorf("开启 on_success 后应发送第 2 次，实际 %d", hits)
	}
}

func TestNotifySyncUnknownChannel(t *testing.T) {
	n := NewNotifier(nil, 5, nil)
	job := &config.JobConfig{ID: "j", Notify: config.NotifyConfig{
		OnFailure: true, Channels: []string{"missing"},
	}}
	errs := n.NotifySync(job, testResult(false))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "不存在") {
		t.Errorf("未知渠道未被报告: %v", errs)
	}
}

func TestNotifySyncReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	n := NewNotifier([]config.NotifyChannel{
		{Name: "ops", Type: "http", URLs: []string{srv.URL}},
	}, 5, nil)
	job := &config.JobConfig{ID: "j", Notify: config.NotifyConfig{
		OnFailure: true, Channels: []string{"ops"},
	}}
	if errs := n.NotifySync(job, testResult(false)); len(errs) != 1 {
		t.Errorf("5xx 未被当作失败: %v", errs)
	}
}

func TestTestChannelAndReload(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier([]config.NotifyChannel{
		{Name: "ops", Type: "http", URLs: []string{srv.URL}},
	}, 5, nil)
	if err := n.TestChannel("ops"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("测试发送应命中 1 次，实际 %d", hits)
	}
	if err := n.TestChannel("nope"); err == nil {
		t.Error("不存在的渠道应返回错误")
	}
	if st := n.Statuses(); len(st) != 1 || !st[0].Ready {
		t.Errorf("渠道状态错误: %+v", st)
	}

	// 热加载后旧渠道必须消失，否则改了 webhook 还会往旧地址发。
	n.Reload([]config.NotifyChannel{{Name: "new", Type: "http", URLs: []string{srv.URL}}}, 5)
	if err := n.TestChannel("ops"); err == nil {
		t.Error("Reload 后旧渠道仍然可用")
	}
	if err := n.TestChannel("new"); err != nil {
		t.Errorf("Reload 后新渠道不可用: %v", err)
	}
}

// 服务停止（Wait）与任务结束（Notify）可能同时发生。
// 旧实现在释放读锁之后才 wg.Add(1)，与 Wait 存在竞态；用 -race 复跑本用例可验证修复。
func TestNotifyConcurrentWithWait(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier([]config.NotifyChannel{
		{Name: "ops", Type: "http", URLs: []string{srv.URL}},
	}, 5, func(string, ...any) {})
	job := &config.JobConfig{ID: "j", Name: "并发", Notify: config.NotifyConfig{
		OnFailure: true, Channels: []string{"ops"},
	}}

	// 先确保至少有一条通知已进入在途状态，避免 Wait 抢在所有 Notify 之前
	// 把 stopped 置为 true（那样 0 条投递也是合法结果，无法验证等待语义）。
	n.Notify(job, testResult(false))
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&hits) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	const nGoroutines = 40
	var producers sync.WaitGroup
	producers.Add(nGoroutines)
	for i := 0; i < nGoroutines; i++ {
		go func() {
			defer producers.Done()
			n.Notify(job, testResult(false))
		}()
	}
	done := make(chan struct{})
	go func() { n.Wait(); close(done) }()
	producers.Wait()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait 未在 10 秒内返回")
	}
	// 至少应有一条在停止前派发的通知完成投递；具体条数允许因停止时点不同而变化。
	if atomic.LoadInt64(&hits) == 0 {
		t.Error("停止前派发的通知全部丢失")
	}
}

func TestBuildErrorsAndBuildMessage(t *testing.T) {
	n := NewNotifier([]config.NotifyChannel{
		{Name: "broken", Type: "dingding", Token: "t"},
	}, 5, nil)
	if errs := n.BuildErrors(); len(errs) != 1 {
		t.Errorf("构造失败未记录: %v", errs)
	}
	if st := n.Statuses(); len(st) != 1 || st[0].Ready {
		t.Errorf("不可用渠道状态错误: %+v", st)
	}

	job := &config.JobConfig{ID: "j", Name: "备份", Command: "echo hi"}
	res := testResult(false)
	res.Output = strings.Repeat("很长", 3000)
	title, body := buildMessage(job, res)
	if !strings.Contains(title, "失败") || !strings.Contains(body, "任务ID: j") {
		t.Errorf("消息内容错误: %q / %q", title, body)
	}
	if len([]byte(body)) > 6000 {
		t.Errorf("正文没有被有效截断，长度 %d", len(body))
	}
}
