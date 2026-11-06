package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	sessionCookieName = "wincron_session"
	sessionTTL        = 24 * time.Hour
)

// sessionPayload 是会话令牌里被签名的内容。
// 用 JSON 而不是 "user|exp" 字符串拼接，避免用户名里出现分隔符导致解析歧义。
// Nonce 必须存在：否则「同一秒内两次登录」会生成完全相同的令牌，
// 其中一次登出被加入吊销名单后，另一次登录的会话也会被连带判为失效。
type sessionPayload struct {
	Username string `json:"u"`
	Expires  int64  `json:"e"`
	Nonce    string `json:"n"`
}

var sessionCounter uint64

func newSessionNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 在 Windows 上实际不会失败；极端情况下用纳秒时间 + 进程内
		// 自增序号兜底，仍然保证同一秒内签发的令牌互不相同。
		b = []byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(),
			atomic.AddUint64(&sessionCounter, 1)))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// sessionRevocations 记录被主动登出的令牌。
// HMAC 令牌本身是无状态的，如果只在浏览器端删 Cookie，被复制走的令牌仍然可用；
// 因此登出时把令牌加入内存吊销名单，服务端立刻不再承认它。
type sessionRevocations struct {
	mu     sync.Mutex
	tokens map[string]int64 // token -> 到期时间（Unix 秒）
}

func newSessionRevocations() *sessionRevocations {
	return &sessionRevocations{tokens: make(map[string]int64)}
}

func (r *sessionRevocations) add(token string, expUnix int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	for k, exp := range r.tokens {
		if exp <= now {
			delete(r.tokens, k)
		}
	}
	r.tokens[token] = expUnix
}

func (r *sessionRevocations) contains(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.tokens[token]
	if !ok {
		return false
	}
	if exp <= time.Now().Unix() {
		delete(r.tokens, token)
		return false
	}
	return true
}

// newSessionKey 在进程启动时生成一次性主密钥。
// 密钥不落盘、不写进配置；进程重启后历史会话自动失效。
func newSessionKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// Windows 上 crypto/rand 实际不会失败；这里仅保证极端情况下仍可用，
		// 用带纳秒时间戳的派生值兜底，比硬编码常量安全。
		sum := sha256.Sum256([]byte(fmt.Sprintf("wincron-fallback-%d", time.Now().UnixNano())))
		return sum[:]
	}
	return key
}

// ---------- 登录失败限流 ----------
// 登录页是暴露在服务端口上的，做一层轻量防爆破；阈值刻意放得宽松，
// 避免正常用户输错两三次就被锁，也方便自动化回归测试。

const (
	loginMaxFailures = 10
	loginLockWindow  = 30 * time.Second
)

type loginAttempt struct {
	count     int
	firstFail time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]*loginAttempt)}
}

func (l *loginLimiter) locked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[ip]
	if !ok {
		return false
	}
	if time.Since(a.firstFail) > loginLockWindow {
		delete(l.attempts, ip)
		return false
	}
	return a.count >= loginMaxFailures
}

// fail 记录一次失败并返回剩余可用次数
func (l *loginLimiter) fail(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[ip]
	if !ok || time.Since(a.firstFail) > loginLockWindow {
		a = &loginAttempt{firstFail: time.Now()}
		l.attempts[ip] = a
	}
	a.count++
	left := loginMaxFailures - a.count
	if left < 0 {
		left = 0
	}
	return left
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}

// ---------- 会话令牌 ----------

// authEnabled 与旧行为保持一致：用户名和密码都为空才算免认证模式。
// 注意这里用 OR 而不是 AND —— 只填了其中一个字段时，绝不能把管理界面
// 悄悄变成免认证，必须继续保持「需要登录」，再由 authConfigError 明确报错。
func (s *Server) authEnabled() bool {
	cfg := s.configManager.GetAppConfig()
	return cfg.WebUsername != "" || cfg.WebPassword != ""
}

// authConfigError 返回半认证配置的说明。用户直接用记事本改 crontab.json 时，
// 很容易只填 web_username 或只填 web_password。旧实现会产生两种坏状态：
//   - 只有用户名：空密码即可登录；
//   - 只有密码：登录接口返回成功，但会话因用户名为空立刻失效，表现为「怎么都登不进去」。
//
// 现在统一 fail-closed：拒绝所有认证，并返回带修复指引的明确错误。
func (s *Server) authConfigError() string {
	cfg := s.configManager.GetAppConfig()
	// 两个都空 = 免认证模式；两个都非空 = 正常认证；只有这两种是完整配置
	if (cfg.WebUsername == "") == (cfg.WebPassword == "") {
		return ""
	}
	missing := "web_password"
	if cfg.WebUsername == "" {
		missing = "web_username"
	}
	return "Web 认证配置不完整：缺少 " + missing + "，web_username 与 web_password 必须同时填写"
}

// authBlockedResponse 供登录接口与鉴权中间件共用的 fail-closed 响应
func authBlockedResponse(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"success":      false,
		"code":         "AUTH_CONFIG_ERROR",
		"error":        msg + "；请补全 crontab.json 中的认证配置后重载，当前拒绝所有登录",
		"config_error": msg,
	})
}

// sessionSecret 把随机主密钥与「当前 Web 密码」混合派生。
// 这样通过 crontab.json 热加载改密码后，之前签发的会话会立即失效，
// 不会出现「改了密码旧的浏览器还登着」这种状态不一致。
func (s *Server) sessionSecret() []byte {
	cfg := s.configManager.GetAppConfig()
	mac := hmac.New(sha256.New, s.sessionKey)
	_, _ = mac.Write([]byte(cfg.WebPassword))
	return mac.Sum(nil)
}

func (s *Server) issueSession(username string, ttl time.Duration) (string, time.Time) {
	exp := time.Now().Add(ttl)
	raw, _ := json.Marshal(sessionPayload{
		Username: username,
		Expires:  exp.Unix(),
		Nonce:    newSessionNonce(),
	})
	payload := base64.RawURLEncoding.EncodeToString(raw)

	mac := hmac.New(sha256.New, s.sessionSecret())
	_, _ = mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, exp
}

func (s *Server) verifySession(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	payload, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, s.sessionSecret())
	_, _ = mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	// 常量时间比较，避免通过响应时间逐字节探测签名
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", false
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false
	}
	if p.Expires <= time.Now().Unix() {
		return "", false
	}
	if s.revocations.contains(token) {
		return "", false
	}

	cfg := s.configManager.GetAppConfig()
	// 用户名变更、认证被关闭、或热加载成半认证配置后，旧会话不再被接受
	if !s.authEnabled() || s.authConfigError() != "" ||
		p.Username != cfg.WebUsername || p.Username == "" {
		return "", false
	}
	return p.Username, true
}

func (s *Server) setSessionCookie(c *gin.Context, token string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	// HttpOnly 阻止 JS 读取令牌；secure=false 是因为本地管理界面默认走 http。
	c.SetCookie(sessionCookieName, token, maxAge, "/", "", false, true)
}

func (s *Server) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", false, true)
}

// ---------- 身份识别 ----------

// checkBasicAuth 兼容既有的脚本 / curl -u 调用方式。
// 关键差异：即使凭据错误也绝不下发 WWW-Authenticate 响应头，
// 否则浏览器会弹出系统原生登录框 —— 那不是页面组件，其凭据由浏览器
// 自己缓存，前端无法感知、无法清除，正是「识别不准确」的根源。
func (s *Server) checkBasicAuth(c *gin.Context) (string, bool) {
	if s.authConfigError() != "" {
		return "", false
	}
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
	if err != nil {
		return "", false
	}
	kv := strings.SplitN(string(decoded), ":", 2)
	if len(kv) != 2 {
		return "", false
	}
	cfg := s.configManager.GetAppConfig()
	userOK := subtle.ConstantTimeCompare([]byte(kv[0]), []byte(cfg.WebUsername)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(kv[1]), []byte(cfg.WebPassword)) == 1
	if !userOK || !passOK {
		return "", false
	}
	return kv[0], true
}

// sessionUser 只认会话 Cookie，专门用于「页面导航」场景（/、/login、auth/status）。
// 浏览器一旦通过旧版原生 Basic 弹窗登录过，会缓存凭据并自动附加 Authorization 头；
// 若页面也认 Basic，用户点「退出登录」后会被缓存凭据自动重新认证，
// 表现就是「登不出去、登录状态识别不准」。因此页面一律只认会话 Cookie。
func (s *Server) sessionUser(c *gin.Context) (string, bool) {
	token, err := c.Cookie(sessionCookieName)
	if err != nil || token == "" {
		return "", false
	}
	return s.verifySession(token)
}

// currentUser 用于 /api 鉴权：会话 Cookie 优先，其次兼容 curl -u 的 Basic Auth。
// 脚本仍然可以沿用原来的调用方式，但页面导航不受它影响。
func (s *Server) currentUser(c *gin.Context) (string, bool) {
	if user, ok := s.sessionUser(c); ok {
		return user, true
	}
	return s.checkBasicAuth(c)
}

// authMiddleware 保护 /api 下的管理接口
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.authEnabled() {
			c.Set("authUser", "anonymous")
			c.Next()
			return
		}
		// 半认证配置：既不开放匿名访问，也不允许任何登录，避免静默降级
		if msg := s.authConfigError(); msg != "" {
			authBlockedResponse(c, msg)
			return
		}
		user, ok := s.currentUser(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success":   false,
				"code":      "UNAUTHENTICATED",
				"error":     "未登录或会话已过期",
				"login_url": "/login",
			})
			return
		}
		c.Set("authUser", user)
		c.Next()
	}
}

// ---------- 登录相关 handler ----------

// handleLoginPage 独立的登录页面（纯 DOM 表单，不再是浏览器原生对话框）
func (s *Server) handleLoginPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.authEnabled() {
		c.Redirect(http.StatusFound, "/")
		return
	}
	if _, ok := s.sessionUser(c); ok {
		c.Redirect(http.StatusFound, "/")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(getLoginHTML()))
}

// handleLogin 校验账号密码并签发会话 Cookie
func (s *Server) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "请求格式错误"})
		return
	}

	if !s.authEnabled() {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "当前未启用登录认证", "username": ""})
		return
	}
	if msg := s.authConfigError(); msg != "" {
		authBlockedResponse(c, msg)
		return
	}

	ip := c.ClientIP()
	if s.limiter.locked(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"error":   "失败次数过多，请稍后再试",
		})
		return
	}

	cfg := s.configManager.GetAppConfig()
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.WebUsername)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(cfg.WebPassword)) == 1
	if !userOK || !passOK {
		left := s.limiter.fail(ip)
		msg := "用户名或密码错误"
		if left <= 3 {
			msg = fmt.Sprintf("用户名或密码错误，还可尝试 %d 次", left)
		}
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "error": msg})
		return
	}

	s.limiter.reset(ip)
	token, exp := s.issueSession(req.Username, sessionTTL)
	s.setSessionCookie(c, token, int(sessionTTL.Seconds()))
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"username":   req.Username,
		"expires_at": exp.Format("2006-01-02 15:04:05"),
		"message":    "登录成功",
	})
}

// handleLogout 吊销服务端会话并清除 Cookie；幂等，不需要先认证
func (s *Server) handleLogout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookieName); err == nil && token != "" {
		// 只删浏览器 Cookie 是不够的：令牌被复制走后仍可通过校验
		s.revocations.add(token, time.Now().Add(sessionTTL).Unix())
	}
	s.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已退出登录"})
}

// handleAuthStatus 无需认证即可访问，供前端判断登录态与显示当前用户。
// 登录页和主页面都靠它做跳转，不再依赖「401 之后递归重试」这种不可靠判断。
func (s *Server) handleAuthStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	enabled := s.authEnabled()
	user, ok := s.sessionUser(c)

	configErr := s.authConfigError()
	resp := gin.H{
		"auth_enabled":  enabled,
		"authenticated": ok || !enabled,
		"username":      "",
		"config_error":  configErr,
	}
	if ok {
		resp["username"] = user
	}
	c.JSON(http.StatusOK, resp)
}
