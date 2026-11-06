package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wincron/internal/config"
	"wincron/internal/executor"
	"wincron/internal/notify"
	"wincron/internal/scheduler"

	"github.com/gin-gonic/gin"
)

// newTestServer 构造一个带真实调度器/执行器但不需要监听端口的测试服务器
func newTestServer(t *testing.T, username, password string) (*Server, *config.ConfigManager, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "crontab.json")

	cfg := config.DefaultConfig()
	cfg.App.WebUsername = username
	cfg.App.WebPassword = password
	cfg.App.LogDir = filepath.Join(dir, "logs")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	cm := config.NewConfigManager(cfgPath)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	exec := executor.NewExecutor(cfg.App.LogDir)
	sched := scheduler.NewScheduler(cm, exec, notify.NewNotifier())
	srv := NewServer(cm, sched, exec, 0)

	return srv, cm, func() { exec.Close() }
}

// do 走完整路由表（含认证中间件）发一次请求，等价于真实 HTTP 请求
func do(t *testing.T, srv *Server, method, path, body string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, ck := range cookies {
		if ck != nil {
			req.AddCookie(ck)
		}
	}
	w := httptest.NewRecorder()
	srv.newRouter().ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%q", err, w.Body.String())
	}
	return m
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

func loginJSON(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return string(b)
}

func loginOK(t *testing.T, srv *Server, user, pass string) *http.Cookie {
	t.Helper()
	w := do(t, srv, http.MethodPost, "/api/login", loginJSON(user, pass), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", w.Code, w.Body.String())
	}
	ck := sessionCookie(w)
	if ck == nil || ck.Value == "" {
		t.Fatal("登录成功但未签发会话 Cookie")
	}
	return ck
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// 1. 未登录访问：主页重定向到独立登录页；API 返回 401 且绝不触发浏览器原生弹窗
func TestUnauthenticatedAccessRedirectsToLoginPage(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	for _, p := range []string{"/", "/index.html"} {
		w := do(t, srv, http.MethodGet, p, "", nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
			t.Fatalf("GET %s 应 302 到 /login, got %d Location=%q", p, w.Code, w.Header().Get("Location"))
		}
	}

	w := do(t, srv, http.MethodGet, "/api/jobs", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 API 应返回 401, got %d", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("禁止下发 WWW-Authenticate（会弹出浏览器原生登录框），got %q", got)
	}
	m := decodeJSON(t, w)
	if m["code"] != "UNAUTHENTICATED" || m["login_url"] != "/login" {
		t.Errorf("401 响应应给出可跳转的登录信息, got %v", m)
	}
}

// 2. 登录页必须是真正的 DOM 表单，而不是 prompt() 或浏览器原生对话框
func TestLoginPageIsRealDOMForm(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	w := do(t, srv, http.MethodGet, "/login", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /login 应返回 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("登录页 Content-Type 异常: %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("登录页应禁止缓存, got %q", cc)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<form id="loginForm"`, `id="username"`, `id="password"`,
		`type="password"`, `id="loginBtn"`, `/api/login`, `/api/auth/status`,
		`id="errorMsg"`, `role="alert"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("登录页缺少页面组件 %q", want)
		}
	}
	if strings.Contains(body, "prompt(") {
		t.Error("登录页不应再使用 prompt() 收集凭据")
	}
}

// 3. 错误账号/密码：返回明确中文提示，且不签发任何会话
func TestLoginWrongCredentials(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	cases := []struct{ user, pass string }{
		{"admin", "wrong-password"},
		{"nobody", "test-only-secret"},
		{"", ""},
	}
	for _, tc := range cases {
		w := do(t, srv, http.MethodPost, "/api/login", loginJSON(tc.user, tc.pass), nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("错误凭据 %q/%q 应返回 401, got %d", tc.user, tc.pass, w.Code)
		}
		m := decodeJSON(t, w)
		if m["success"] != false || m["error"] == "" || m["error"] == nil {
			t.Errorf("401 响应应包含明确错误提示, got %v", m)
		}
		if ck := sessionCookie(w); ck != nil && ck.Value != "" {
			t.Errorf("错误凭据不应签发会话 Cookie: %v", ck)
		}
	}

	w := do(t, srv, http.MethodGet, "/api/jobs", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Error("连续登录失败后，API 仍应处于未认证状态")
	}
}

// 4. 登录成功：签发 HttpOnly 会话，主页/API 均可访问，已登录访问 /login 自动回主页
func TestLoginSuccessAndCookieAttributes(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	w := do(t, srv, http.MethodPost, "/api/login", loginJSON("admin", "test-only-secret"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("正确凭据应登录成功, got %d %s", w.Code, w.Body.String())
	}
	m := decodeJSON(t, w)
	if m["success"] != true || m["username"] != "admin" || m["expires_at"] == "" {
		t.Errorf("登录响应字段异常: %v", m)
	}

	ck := sessionCookie(w)
	if ck == nil {
		t.Fatal("缺少会话 Cookie")
	}
	if !ck.HttpOnly {
		t.Error("会话 Cookie 必须是 HttpOnly")
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("Cookie SameSite 应为 Lax, got %v", ck.SameSite)
	}
	if ck.Path != "/" {
		t.Errorf("Cookie Path 应为 /, got %q", ck.Path)
	}
	if ck.MaxAge <= 0 {
		t.Errorf("Cookie 应设置正的 MaxAge, got %d", ck.MaxAge)
	}
	if ck.Secure {
		t.Error("本地 http 管理界面不应强制 Secure，否则浏览器不会保存 Cookie")
	}

	if got := do(t, srv, http.MethodGet, "/api/jobs", "", nil, ck); got.Code != http.StatusOK {
		t.Errorf("带会话访问 API 应 200, got %d %s", got.Code, got.Body.String())
	}
	idx := do(t, srv, http.MethodGet, "/", "", nil, ck)
	if idx.Code != http.StatusOK || !strings.Contains(idx.Body.String(), "WinCron 定时任务管理") {
		t.Errorf("带会话访问主页应返回管理界面, got %d", idx.Code)
	}
	back := do(t, srv, http.MethodGet, "/login", "", nil, ck)
	if back.Code != http.StatusFound || back.Header().Get("Location") != "/" {
		t.Errorf("已登录访问 /login 应跳回主页, got %d %q", back.Code, back.Header().Get("Location"))
	}
}

// 5. auth/status 是公开的状态源，前端靠它判断登录态，不再靠 401 递归重试
func TestAuthStatus(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	w := do(t, srv, http.MethodGet, "/api/auth/status", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("auth/status 应公开可访问, got %d", w.Code)
	}
	m := decodeJSON(t, w)
	if m["auth_enabled"] != true || m["authenticated"] != false || m["username"] != "" {
		t.Errorf("未登录状态异常: %v", m)
	}

	ck := loginOK(t, srv, "admin", "test-only-secret")
	m = decodeJSON(t, do(t, srv, http.MethodGet, "/api/auth/status", "", nil, ck))
	if m["authenticated"] != true || m["username"] != "admin" {
		t.Errorf("已登录状态异常: %v", m)
	}
}

// 6. 退出登录：服务端吊销令牌，旧的 Cookie 立即失效（不只是浏览器删 Cookie）
func TestLogoutRevokesSession(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	ck := loginOK(t, srv, "admin", "test-only-secret")

	w := do(t, srv, http.MethodPost, "/api/logout", "", nil, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应返回 200, got %d", w.Code)
	}
	cleared := sessionCookie(w)
	if cleared == nil {
		t.Fatal("登出响应应下发清除 Cookie 的 Set-Cookie")
	}
	if cleared.Value != "" {
		t.Errorf("登出 Cookie 值应被清空, got %q", cleared.Value)
	}

	w = do(t, srv, http.MethodGet, "/api/jobs", "", nil, ck)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("登出后旧令牌必须立即失效, got %d", w.Code)
	}
	w = do(t, srv, http.MethodGet, "/", "", nil, ck)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("登出后主页应跳登录页, got %d", w.Code)
	}
	m := decodeJSON(t, do(t, srv, http.MethodGet, "/api/auth/status", "", nil, ck))
	if m["authenticated"] != false {
		t.Errorf("登出后 auth/status 应为未认证, got %v", m)
	}
}

// 7. Basic Auth 仅保留给脚本调用；页面导航不认它，避免浏览器缓存凭据导致「登不出去」
func TestBasicAuthCompatibilityForScriptsOnly(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	h := map[string]string{"Authorization": basicHeader("admin", "test-only-secret")}
	if w := do(t, srv, http.MethodGet, "/api/jobs", "", h); w.Code != http.StatusOK {
		t.Errorf("脚本用 Basic Auth 访问 API 应兼容, got %d", w.Code)
	}

	bad := map[string]string{"Authorization": basicHeader("admin", "bad")}
	w := do(t, srv, http.MethodGet, "/api/jobs", "", bad)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("错误 Basic 凭据应 401, got %d", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("Basic 失败也不得下发 WWW-Authenticate, got %q", got)
	}

	// 关键回归：浏览器可能缓存旧 Basic 凭据并自动附加，页面绝不能被它「自动登录」
	w = do(t, srv, http.MethodGet, "/", "", h)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("页面导航不应接受 Basic 凭据, got %d %q", w.Code, w.Header().Get("Location"))
	}
	w = do(t, srv, http.MethodGet, "/login", "", h)
	if w.Code != http.StatusOK {
		t.Errorf("带缓存 Basic 凭据访问登录页应正常显示表单, got %d", w.Code)
	}
	m := decodeJSON(t, do(t, srv, http.MethodGet, "/api/auth/status", "", h))
	if m["authenticated"] != false {
		t.Errorf("auth/status 不应被 Basic 凭据欺骗, got %v", m)
	}
}

// 8. 免认证模式（用户名密码都为空）：一切照常可用，登录页自动跳回主页
func TestNoAuthMode(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "", "")
	defer cleanup()

	if w := do(t, srv, http.MethodGet, "/", "", nil); w.Code != http.StatusOK {
		t.Errorf("免认证模式主页应直接可访问, got %d", w.Code)
	}
	if w := do(t, srv, http.MethodGet, "/api/jobs", "", nil); w.Code != http.StatusOK {
		t.Errorf("免认证模式 API 应直接可访问, got %d", w.Code)
	}
	w := do(t, srv, http.MethodGet, "/login", "", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("免认证模式登录页应跳回主页, got %d", w.Code)
	}
	m := decodeJSON(t, do(t, srv, http.MethodGet, "/api/auth/status", "", nil))
	if m["auth_enabled"] != false || m["authenticated"] != true {
		t.Errorf("免认证模式状态异常: %v", m)
	}
	m = decodeJSON(t, do(t, srv, http.MethodPost, "/api/login", loginJSON("any", "any"), nil))
	if m["success"] != true {
		t.Errorf("免认证模式登录接口应直接成功: %v", m)
	}
}

// 9. 令牌被篡改、伪造、过期，一律拒绝
func TestTamperedForgedAndExpiredTokensRejected(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	ck := loginOK(t, srv, "admin", "test-only-secret")
	parts := strings.SplitN(ck.Value, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("令牌格式异常: %q", ck.Value)
	}

	badTokens := []string{
		"", "abc", "a.b", ck.Value + "x",
		parts[0] + ".forged-signature",
		"X" + parts[0][1:] + "." + parts[1], // 篡改 payload，签名不再匹配
	}
	for _, token := range badTokens {
		w := do(t, srv, http.MethodGet, "/api/jobs", "", nil, &http.Cookie{Name: sessionCookieName, Value: token})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("非法令牌 %q 应被拒绝, got %d", token, w.Code)
		}
	}

	expired, _ := srv.issueSession("admin", -time.Hour)
	w := do(t, srv, http.MethodGet, "/api/jobs", "", nil, &http.Cookie{Name: sessionCookieName, Value: expired})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("过期令牌应被拒绝, got %d", w.Code)
	}

	// 用户名不是当前配置的账号，即使签名正确也不能通过
	other, _ := srv.issueSession("someone-else", sessionTTL)
	w = do(t, srv, http.MethodGet, "/api/jobs", "", nil, &http.Cookie{Name: sessionCookieName, Value: other})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("非当前账号的令牌应被拒绝, got %d", w.Code)
	}
}

// 10. 通过配置热加载改密码后，历史会话立即失效（密钥由当前密码派生）
func TestPasswordChangeInvalidatesOldSessions(t *testing.T) {
	srv, cm, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	ck := loginOK(t, srv, "admin", "test-only-secret")
	if w := do(t, srv, http.MethodGet, "/api/jobs", "", nil, ck); w.Code != http.StatusOK {
		t.Fatalf("改密码前会话应有效, got %d", w.Code)
	}

	raw, err := os.ReadFile(cm.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.CrontabFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.App.WebPassword = "changed-secret"
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cm.Path(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv, http.MethodGet, "/api/jobs", "", nil, ck)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("改密码后旧会话必须立即失效, got %d", w.Code)
	}
	ck2 := loginOK(t, srv, "admin", "changed-secret")
	if w := do(t, srv, http.MethodGet, "/api/jobs", "", nil, ck2); w.Code != http.StatusOK {
		t.Errorf("用新密码登录后应正常访问, got %d", w.Code)
	}
}

// 11. 登录失败限流
func TestLoginRateLimit(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	for i := 0; i < loginMaxFailures; i++ {
		w := do(t, srv, http.MethodPost, "/api/login", loginJSON("admin", "wrong"), nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应返回 401, got %d %s", i+1, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{loginJSON("admin", "wrong"), loginJSON("admin", "test-only-secret")} {
		w := do(t, srv, http.MethodPost, "/api/login", body, nil)
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("达到失败上限后应返回 429, got %d", w.Code)
		}
	}
}

// 12. 登录请求体校验
func TestLoginRequestValidation(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	w := do(t, srv, http.MethodPost, "/api/login", "not-json", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法 JSON 应返回 400, got %d", w.Code)
	}
	for _, body := range []string{`{"username":"admin"}`, `{"password":"test-only-secret"}`, `{}`} {
		w = do(t, srv, http.MethodPost, "/api/login", body, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("缺少凭据 %s 应返回 401, got %d", body, w.Code)
		}
	}
}

// 13. 内嵌页面源码里不得残留浏览器原生认证 / prompt 相关实现
func TestEmbeddedPagesContainNoBrowserNativeAuth(t *testing.T) {
	idx := getIndexHTML()
	for _, bad := range []string{"prompt(", "btoa(", "WWW-Authenticate", "Authorization", "credentials ="} {
		if strings.Contains(idx, bad) {
			t.Errorf("index.html 仍残留旧认证代码 %q", bad)
		}
	}
	for _, want := range []string{"/api/auth/status", "logoutBtn", "currentUser", "showToast", "window.location.replace('/login')"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html 缺少会话相关实现 %q", want)
		}
	}

	login := getLoginHTML()
	for _, want := range []string{`<form id="loginForm"`, `type="password"`, `/api/login`, `id="errorMsg"`} {
		if !strings.Contains(login, want) {
			t.Errorf("login.html 缺少 %q", want)
		}
	}
	if strings.Contains(login, "prompt(") || strings.Contains(login, "WWW-Authenticate") {
		t.Error("login.html 不应使用浏览器原生认证")
	}
}

// 14. 同一秒内两次登录必须签发不同令牌，避免登出时相互牵连。
// 令牌带随机 nonce：若同一秒内两次登录拿到相同令牌，其中一次登出进吊销
// 名单后，另一次登录的会话也会莫名 401。
func TestSessionTokensAreUniqueAndLogoutOnlyRevokesOne(t *testing.T) {
	srv, _, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	a := loginOK(t, srv, "admin", "test-only-secret")
	b := loginOK(t, srv, "admin", "test-only-secret")
	if a.Value == b.Value {
		t.Fatal("同一秒内两次登录签发了完全相同的会话令牌")
	}

	w := do(t, srv, http.MethodPost, "/api/logout", "", nil, a)
	if w.Code != http.StatusOK {
		t.Fatalf("登出失败: %d", w.Code)
	}
	if w = do(t, srv, http.MethodGet, "/api/status", "", nil, a); w.Code != http.StatusUnauthorized {
		t.Errorf("被登出的会话应失效，实际 %d", w.Code)
	}
	if w = do(t, srv, http.MethodGet, "/api/status", "", nil, b); w.Code != http.StatusOK {
		t.Errorf("另一会话不应被连带吊销，实际 %d: %s", w.Code, w.Body.String())
	}
}

func setWebCredentials(t *testing.T, cm *config.ConfigManager, user, pass string) {
	t.Helper()
	cfg := cm.Get()
	cfg.App.WebUsername = user
	cfg.App.WebPassword = pass
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cm.Path(), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
}

// 15. 只填用户名 / 只填密码的半认证配置要 fail-closed，
// 既不能空密码登录，也不能「登录成功但会话立即失效」，更不能敞开匿名访问。
func TestHalfAuthConfigFailsClosedAndRecovers(t *testing.T) {
	srv, cm, cleanup := newTestServer(t, "admin", "test-only-secret")
	defer cleanup()

	old := loginOK(t, srv, "admin", "test-only-secret")
	setWebCredentials(t, cm, "admin", "")

	w := do(t, srv, http.MethodGet, "/api/auth/status", "", nil)
	m := decodeJSON(t, w)
	if m["config_error"] == "" || m["config_error"] == nil {
		t.Errorf("auth/status 未暴露配置错误: %v", m)
	}

	w = do(t, srv, http.MethodPost, "/api/login", loginJSON("admin", ""), nil)
	if w.Code != http.StatusServiceUnavailable || sessionCookie(w) != nil {
		t.Errorf("半认证配置应返回 503 且不签发 Cookie，实际 %d: %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		cookie  *http.Cookie
	}{
		{"旧会话", nil, old},
		{"Basic Auth", map[string]string{"Authorization": basicHeader("admin", "")}, nil},
	} {
		w = do(t, srv, http.MethodGet, "/api/status", "", tc.headers, tc.cookie)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s 在半认证配置下应被 503 拒绝，实际 %d", tc.name, w.Code)
		}
	}

	setWebCredentials(t, cm, "admin", "test-only-secret")
	w = do(t, srv, http.MethodPost, "/api/login", loginJSON("admin", "test-only-secret"), nil)
	if w.Code != http.StatusOK {
		t.Errorf("补全配置后应恢复登录，实际 %d: %s", w.Code, w.Body.String())
	}
}
