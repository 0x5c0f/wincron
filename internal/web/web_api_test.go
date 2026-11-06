package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"wincron/internal/config"
)

// 日志分页接口必须给出 total / offset / limit / has_more / max_entries，
// 并把非法参数夹紧，避免一次请求把全部历史记录序列化回浏览器。
func TestLogPagingAPIResponseAndClamping(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()
	ck := loginOK(t, srv, "admin", "test-only-secret")

	cases := []struct {
		name       string
		query      string
		wantOffset float64
		wantLimit  float64
	}{
		{"默认", "", 0, 20},
		{"正常分页", "?limit=50&offset=5", 5, 50},
		{"负数回退", "?limit=-1&offset=-3", 0, 20},
		{"limit 超上限夹到 100", "?limit=1000", 0, 100},
		{"offset 超出总数", "?offset=999", 999, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, srv, http.MethodGet, "/api/logs"+tc.query, "", nil, ck)
			if w.Code != http.StatusOK {
				t.Fatalf("GET /api/logs 返回 %d: %s", w.Code, w.Body.String())
			}
			m := decodeJSON(t, w)
			if m["total"] != float64(0) || m["count"] != float64(0) || m["has_more"] != false {
				t.Errorf("空执行器的分页状态不正确: %v", m)
			}
			if m["offset"] != tc.wantOffset || m["limit"] != tc.wantLimit {
				t.Errorf("分页参数夹紧错误: offset=%v limit=%v, want %v/%v",
					m["offset"], m["limit"], tc.wantOffset, tc.wantLimit)
			}
			if m["max_entries"] != float64(500) {
				t.Errorf("max_entries 应回传配置值 500，实际 %v", m["max_entries"])
			}
			if _, ok := m["logs"].([]interface{}); !ok {
				t.Errorf("logs 应为数组，实际 %T", m["logs"])
			}
		})
	}

	// 按任务查询走的是另一个路由，响应结构必须一致
	w := do(t, srv, http.MethodGet, "/api/logs/some-job?limit=5&offset=0", "", nil, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/logs/:jobId 返回 %d", w.Code)
	}
	m := decodeJSON(t, w)
	if m["limit"] != float64(5) || m["total"] != float64(0) || m["max_entries"] != float64(500) {
		t.Errorf("按任务分页响应异常: %v", m)
	}
}

// 通知渠道新增 / 详情 / 更新 / 删除 / 设置，以及「列表绝不回传凭据」
func TestNotifyChannelCRUDAPI(t *testing.T) {
	srv, cm, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	if w := do(t, srv, http.MethodGet, "/api/notify/channels", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("未登录访问渠道列表应 401，实际 %d", w.Code)
	}
	ck := loginOK(t, srv, "admin", "test-only-secret")

	add := `{"name":"web-http","type":"http","urls":["http://127.0.0.1:9/hook"],"token":"e2e-secret-token"}`
	w := do(t, srv, http.MethodPost, "/api/notify/channels", add, nil, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("新增渠道失败 %d: %s", w.Code, w.Body.String())
	}

	// 列表只给名称/类型/必填字段，凭据和 URL 都不能出现在响应里
	w = do(t, srv, http.MethodGet, "/api/notify/channels", "", nil, ck)
	listBody := w.Body.String()
	for _, secret := range []string{"e2e-secret-token", "http://127.0.0.1:9/hook"} {
		if strings.Contains(listBody, secret) {
			t.Errorf("列表响应泄露了凭据/URL: %s", secret)
		}
	}
	m := decodeJSON(t, w)
	channels, ok := m["channels"].([]interface{})
	if !ok || len(channels) != 1 {
		t.Fatalf("列表应含 1 个渠道，实际 %v", m["channels"])
	}
	first := channels[0].(map[string]interface{})
	if first["name"] != "web-http" || first["type"] != "http" {
		t.Errorf("列表渠道字段异常: %v", first)
	}
	requiredMap, ok := m["required_map"].(map[string]interface{})
	if !ok {
		t.Fatal("列表缺少 required_map，前端无法按类型动态渲染必填字段")
	}
	if _, ok := requiredMap["wecom"]; !ok {
		t.Errorf("required_map 缺少 wecom: %v", requiredMap)
	}
	if m["notify_timeout"] != float64(10) {
		t.Errorf("列表应回传 notify_timeout=10，实际 %v", m["notify_timeout"])
	}

	// 单个渠道详情才返回完整定义，且必须禁止缓存
	w = do(t, srv, http.MethodGet, "/api/notify/channels/web-http", "", nil, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("渠道详情返回 %d: %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("含凭据的详情响应必须 no-store，实际 %q", cc)
	}
	detail := decodeJSON(t, w)["channel"].(map[string]interface{})
	if detail["token"] != "e2e-secret-token" {
		t.Errorf("详情未返回完整定义: %v", detail)
	}
	if w := do(t, srv, http.MethodGet, "/api/notify/channels/missing", "", nil, ck); w.Code != http.StatusNotFound {
		t.Errorf("不存在的渠道详情应 404，实际 %d", w.Code)
	}

	// 整体更新：字段被替换，不残留旧值
	update := `{"name":"web-http","type":"http","urls":["http://127.0.0.1:10/hook2"],"timeout":7}`
	if w := do(t, srv, http.MethodPut, "/api/notify/channels/web-http", update, nil, ck); w.Code != http.StatusOK {
		t.Fatalf("更新渠道失败 %d: %s", w.Code, w.Body.String())
	}
	detail = decodeJSON(t, do(t, srv, http.MethodGet, "/api/notify/channels/web-http", "", nil, ck))["channel"].(map[string]interface{})
	if detail["timeout"] != float64(7) {
		t.Errorf("timeout 未更新: %v", detail)
	}
	if _, exists := detail["token"]; exists {
		t.Error("整体更新后旧 token 仍在，语义不是替换")
	}

	// 改名与非法定义都要 400，且不能写脏配置
	rename := `{"name":"renamed","type":"http","urls":["http://127.0.0.1:9/hook"]}`
	if w := do(t, srv, http.MethodPut, "/api/notify/channels/web-http", rename, nil, ck); w.Code != http.StatusBadRequest {
		t.Errorf("改名应 400，实际 %d", w.Code)
	}
	invalid := `{"name":"bad","type":"http"}`
	if w := do(t, srv, http.MethodPost, "/api/notify/channels", invalid, nil, ck); w.Code != http.StatusBadRequest {
		t.Errorf("缺少必填字段应 400，实际 %d", w.Code)
	}

	// 发送超时：非法值拒绝，合法值写回文件
	if w := do(t, srv, http.MethodPut, "/api/notify/settings", `{"notify_timeout":0}`, nil, ck); w.Code != http.StatusBadRequest {
		t.Errorf("超时 0 应 400，实际 %d", w.Code)
	}
	if w := do(t, srv, http.MethodPut, "/api/notify/settings", `{"notify_timeout":45}`, nil, ck); w.Code != http.StatusOK {
		t.Fatalf("合法超时保存失败 %d: %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(cm.Path())
	if err != nil {
		t.Fatal(err)
	}
	var file config.CrontabFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.App.NotifyTimeout != 45 || len(file.App.NotifyChannels) != 1 {
		t.Errorf("Web 改动未持久化: timeout=%d channels=%d", file.App.NotifyTimeout, len(file.App.NotifyChannels))
	}

	// 删除后列表为空，重复删除报错
	if w := do(t, srv, http.MethodDelete, "/api/notify/channels/web-http", "", nil, ck); w.Code != http.StatusOK {
		t.Fatalf("删除渠道失败 %d: %s", w.Code, w.Body.String())
	}
	m = decodeJSON(t, do(t, srv, http.MethodGet, "/api/notify/channels", "", nil, ck))
	if got := m["channels"].([]interface{}); len(got) != 0 {
		t.Errorf("删除后列表仍非空: %v", got)
	}
	if w := do(t, srv, http.MethodDelete, "/api/notify/channels/web-http", "", nil, ck); w.Code != http.StatusBadRequest {
		t.Errorf("重复删除应 400，实际 %d", w.Code)
	}
}

// 被任务引用的渠道不允许从 Web 删除，否则任务的通知会静默丢失
func TestNotifyChannelDeleteRejectedWhenJobReferences(t *testing.T) {
	srv, cm, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()
	ck := loginOK(t, srv, "admin", "test-only-secret")

	cfg := cm.Get()
	cfg.App.NotifyChannels = []config.NotifyChannel{{
		Name: "used", Type: "http", URLs: []string{"http://127.0.0.1:9/hook"},
	}}
	cfg.Jobs = []config.JobConfig{{
		ID:      "job-ref",
		Name:    "引用任务",
		Cron:    "* * * * *",
		Command: "echo hi",
		Enabled: true,
		Notify:  config.NotifyConfig{Channels: []string{"used"}},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cm.Path(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv, http.MethodDelete, "/api/notify/channels/used", "", nil, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("删除被引用渠道应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "job-ref") {
		t.Errorf("错误应指出引用任务: %s", w.Body.String())
	}
}

// 面向浏览器的半认证错误只给中性文案；详细字段名只留在 CLI 校验输出里
func TestBrowserAuthConfigErrorIsNeutral(t *testing.T) {
	srv, cm, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	cfg := cm.Get()
	cfg.App.WebPassword = ""
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cm.Path(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	m := decodeJSON(t, do(t, srv, http.MethodGet, "/api/auth/status", "", nil))
	msg, _ := m["config_error"].(string)
	if msg == "" {
		t.Fatal("auth/status 未暴露配置错误")
	}
	for _, bad := range []string{"crontab.json", "web_username", "web_password"} {
		if strings.Contains(msg, bad) {
			t.Errorf("面向浏览器的错误不应暴露配置细节 %q: %s", bad, msg)
		}
	}
	blocked := do(t, srv, http.MethodPost, "/api/login", loginJSON("admin", ""), nil)
	if blocked.Code != http.StatusServiceUnavailable {
		t.Errorf("半认证配置登录应 503，实际 %d", blocked.Code)
	}
	for _, bad := range []string{"crontab.json", "web_username", "web_password"} {
		if strings.Contains(blocked.Body.String(), bad) {
			t.Errorf("登录响应不应暴露配置细节 %q: %s", bad, blocked.Body.String())
		}
	}
}

// 前端六项细节优化的静态断言：无原生下拉、有分页/自适应卡片/渠道配置/tab 记忆
func TestIndexPageUIUpgrades(t *testing.T) {
	idx := getIndexHTML()
	for _, bad := range []string{
		"<select", "</select>",
		"onchange=\"switchTab('jobs', this)\"",
		"max-height:220px;overflow:auto",
		"substring(0,200)",
		"crontab.json 的 app.notify_channels 中添加",
	} {
		if strings.Contains(idx, bad) {
			t.Errorf("index.html 仍残留旧实现 %q", bad)
		}
	}
	for _, want := range []string{
		"class WcSelect", "wc-select-menu", "wcSelectInstances",
		"history.pushState", "popstate", "tabFromHash", "applyTab",
		"logsPagerInfo", "logsLimitSelect", "gotoLogPage", "has_more", "max_entries",
		"env-grid", "toggleEnvValue", "env-full",
		"channelModal", "renderChannelTypeFields", "saveChannel", "deleteChannel",
		"required_map", "/notify/settings", "/notify/channels/",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html 缺少本轮优化实现 %q", want)
		}
	}
}

// 登录页不再展示账号来自哪个文件、哪些配置字段
func TestLoginPageHidesConfigSource(t *testing.T) {
	login := getLoginHTML()
	for _, bad := range []string{
		"crontab.json", "web_username", "web_password",
		"请输入配置文件中设置的账号和密码",
		"账号来自",
	} {
		if strings.Contains(login, bad) {
			t.Errorf("登录页仍暴露配置来源 %q", bad)
		}
	}
	for _, want := range []string{
		"请输入管理员账号和密码", "showConfigError()", "登录会话有效期 24 小时",
	} {
		if !strings.Contains(login, want) {
			t.Errorf("登录页缺少 %q", want)
		}
	}
}
