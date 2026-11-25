package main

import (
	"bytes"
	"html/template"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCaptchaIssueAndVerify 验证验证码的签发与校验。
func TestCaptchaIssueAndVerify(t *testing.T) {
	s := newCaptchaStore()
	token, code := s.issue()
	if token == "" {
		t.Fatal("签发出来的令牌不应为空")
	}
	if len(code) != captchaDigits {
		t.Fatalf("验证码长度 = %d，期望 %d", len(code), captchaDigits)
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Fatalf("验证码应全是数字，得到 %q", code)
		}
	}
	if !s.verify(token, code) {
		t.Fatal("正确的验证码应当通过校验")
	}
	// 一次一用：同一个令牌不能第二次用。
	// 不这样的话，攻击者可以拿同一张图反复枚举答案。
	if s.verify(token, code) {
		t.Fatal("同一个验证码不能用第二次")
	}
}

// TestCaptchaRejectsWrongInput 验证错误答案与空答案都被拒。
func TestCaptchaRejectsWrongInput(t *testing.T) {
	s := newCaptchaStore()
	token, code := s.issue()
	wrong := "0000"
	if code == wrong {
		wrong = "1111"
	}
	if s.verify(token, wrong) {
		t.Fatal("错误的验证码不应通过")
	}
	// 空答案不能顶替正确答案。
	token2, _ := s.issue()
	if s.verify(token2, "") {
		t.Fatal("空验证码不应通过")
	}
	// 令牌为空也不行。
	if s.verify("", "1234") {
		t.Fatal("没有令牌时不应通过")
	}
}

// TestCaptchaExpires 验证过期的验证码不再有效。
func TestCaptchaExpires(t *testing.T) {
	s := newCaptchaStore()
	token, code := s.issue()
	s.mu.Lock()
	e := s.items[token]
	e.expires = time.Now().Add(-time.Second)
	s.items[token] = e
	s.mu.Unlock()
	if s.verify(token, code) {
		t.Fatal("过期的验证码不应通过")
	}
}

// TestCaptchaStoreCapsSize 验证验证码条数有上限，攻击者不能靠狂取图撑爆内存。
func TestCaptchaStoreCapsSize(t *testing.T) {
	s := newCaptchaStore()
	for i := 0; i < captchaMax+50; i++ {
		s.issue()
	}
	s.mu.Lock()
	n := len(s.items)
	s.mu.Unlock()
	if n > captchaMax {
		t.Fatalf("有效验证码条数 = %d，超过上限 %d", n, captchaMax)
	}
}

// TestCaptchaPNGDecodes 验证生成的是一张能被解码的 PNG，尺寸固定。
func TestCaptchaPNGDecodes(t *testing.T) {
	data := captchaPNG("1234")
	if len(data) == 0 {
		t.Fatal("PNG 内容为空")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	if img.Bounds().Dx() != captchaWidth || img.Bounds().Dy() != captchaHeight {
		t.Fatalf("图片尺寸 = %dx%d，期望 %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), captchaWidth, captchaHeight)
	}
	// 画面上得有明显比背景深的墨迹，不然就是一张空白图。
	// 判据按「比背景深且各通道都够暗」来定：噪点是中性灰（R=G=B），
	// 调色板里的深蓝、深紫也够暗——所以三个通道一起看才不会误判。
	ink := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, _ := img.At(x, y).RGBA()
			if r>>8 < 100 && g>>8 < 100 && bb>>8 < 170 {
				ink++
			}
		}
	}
	if ink < 500 {
		t.Fatalf("图上的墨迹像素只有 %d 个，数字可能没画出来", ink)
	}
}

// TestCaptchaRequired 验证哪些来源要验证码、哪些不要。
func TestCaptchaRequired(t *testing.T) {
	a := &App{login: newLoginGuard(), captcha: newCaptchaStore()}
	cases := []struct {
		name       string
		remoteAddr string
		host       string
		want       bool
	}{
		{"公网来源要验证码", "203.0.113.7:44321", "ts.example.com", true},
		{"回环来源本机 Host 免验证码", "127.0.0.1:54321", "127.0.0.1:18080", false},
		{"回环来源 localhost 免验证码", "127.0.0.1:54321", "localhost:18080", false},
		{"回环来源但公网 Host 仍要验证码", "127.0.0.1:54321", "ts.example.com", true},
		{"IPv6 回环免验证码", "[::1]:54321", "[::1]:18080", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.host+"/login", nil)
		r.RemoteAddr = c.remoteAddr
		r.Host = c.host
		if got := a.captchaRequired(r); got != c.want {
			t.Fatalf("%s: captchaRequired = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestCaptchaSkipsTrustedSource 验证可信来源不用输验证码。
func TestCaptchaSkipsTrustedSource(t *testing.T) {
	t.Setenv(loginTrustedEnv, "203.0.113.0/24")
	a := &App{login: newLoginGuard(), captcha: newCaptchaStore()}
	r := httptest.NewRequest(http.MethodPost, "http://ts.example.com/login", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	if a.captchaRequired(r) {
		t.Fatal("可信来源不应要求验证码")
	}
}

// TestLoginPostRequiresCaptchaFromPublicSource 验证公网来源必须先过验证码。
func TestLoginPostRequiresCaptchaFromPublicSource(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("admin", "testpass123", "admin"); err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("").Parse(`{{define "login"}}{{.Error}}{{end}}`))
	a := &App{
		store: store, sessions: NewSessionManager(store.DB, false),
		login: newLoginGuard(), tmpl: tmpl, captcha: newCaptchaStore(),
	}

	// 少了验证码：不能登录成功。
	r := httptest.NewRequest(http.MethodPost, "http://ts.example.com/login",
		strings.NewReader("username=admin&password=testpass123"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "203.0.113.7:1234"
	w := httptest.NewRecorder()
	a.loginPost(w, r)
	if w.Code == http.StatusSeeOther {
		t.Fatal("缺少验证码时不应登录成功")
	}
	if !strings.Contains(w.Body.String(), "验证码错误") {
		t.Fatalf("应提示验证码错误，实际输出: %q", w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name != captchaCookie {
			t.Fatalf("校验失败时不应下发会话 Cookie，却拿到了 %s", c.Name)
		}
	}

	// 带上正确的验证码：放行。
	token, code := a.captcha.issue()
	r2 := httptest.NewRequest(http.MethodPost, "http://ts.example.com/login",
		strings.NewReader("username=admin&password=testpass123&captcha="+code))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.RemoteAddr = "203.0.113.7:1234"
	r2.AddCookie(&http.Cookie{Name: captchaCookie, Value: token})
	w2 := httptest.NewRecorder()
	a.loginPost(w2, r2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("验证码正确时应登录成功，状态码 = %d，输出 %q", w2.Code, w2.Body.String())
	}
}

// TestLoginPostSkipsCaptchaFromLoopback 验证本机来源不受验证码影响。
//
// 本机 e2e、单机部署、运维脚本都走回环，不能因为加了验证码就登不进去。
func TestLoginPostSkipsCaptchaFromLoopback(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("admin", "testpass123", "admin"); err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("").Parse(`{{define "login"}}{{.Error}}{{end}}`))
	a := &App{
		store: store, sessions: NewSessionManager(store.DB, false),
		login: newLoginGuard(), tmpl: tmpl, captcha: newCaptchaStore(),
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/login",
		strings.NewReader("username=admin&password=testpass123"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:54321"
	r.Host = "127.0.0.1:18080"
	w := httptest.NewRecorder()
	a.loginPost(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("回环来源登录应成功，状态码 = %d，输出 %q", w.Code, w.Body.String())
	}
}

// TestLoginPageShowsCaptchaForPublicSource 验证公网来源的登录页会出现验证码。
func TestLoginPageShowsCaptchaForPublicSource(t *testing.T) {
	a := &App{login: newLoginGuard(), captcha: newCaptchaStore()}

	r := httptest.NewRequest(http.MethodGet, "http://ts.example.com/login", nil)
	r.RemoteAddr = "203.0.113.7:1234"
	data := a.loginPageData(r, "zh")
	if data["Captcha"] != true {
		t.Fatal("公网来源的登录页应当要求验证码")
	}
	if data["CaptchaNonce"] == "" {
		t.Fatal("验证码图片应当带一个防缓存的随机参数")
	}

	r2 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/login", nil)
	r2.RemoteAddr = "127.0.0.1:54321"
	r2.Host = "127.0.0.1:18080"
	data2 := a.loginPageData(r2, "zh")
	if _, ok := data2["Captcha"]; ok {
		t.Fatal("回环来源的登录页不该出现验证码")
	}
}

// TestStaticHandlerRevalidates 验证静态资源不会在浏览器里长期缓存。
//
// 样式和脚本的 URL 不带版本号，浏览器会用「文件年龄的 10%」做启发式缓存：
// 上线新版样式后，老浏览器的缓存还能撑十几个小时，用户看到的还是旧界面
// （本次加验证码就踩到了：新样式没生效，验证码图片错位换行）。
func TestStaticHandlerRevalidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	h := a.staticHandler(dir)
	r := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q，期望 no-cache", got)
	}
}

// TestAssetVersionChangesWithContent 验证静态资源版本号随内容变化。
//
// 版本号要放进 URL 里：老浏览器缓存里的旧样式，URL 不变就一直被复用。
func TestAssetVersionChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	v1 := assetVersion(dir)
	if v1 == "" || v1 == "0" {
		t.Fatalf("版本号不应为空: %q", v1)
	}
	if v2 := assetVersion(dir); v2 != v1 {
		t.Fatalf("内容没变时版本号应保持一致: %q != %q", v1, v2)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v3 := assetVersion(dir); v3 == v1 {
		t.Fatal("内容变了版本号必须跟着变，否则浏览器还吃旧缓存")
	}
	// 目录不存在时也不能崩，给个占位版本号。
	if v := assetVersion(filepath.Join(dir, "no-such-dir")); v == "" {
		t.Fatal("目录不存在时也应返回占位版本号")
	}
}

// TestCaptchaImageSetsCookie 验证取验证码图片时会下发 HttpOnly 令牌 Cookie。
func TestCaptchaImageSetsCookie(t *testing.T) {
	a := &App{login: newLoginGuard(), captcha: newCaptchaStore()}
	r := httptest.NewRequest(http.MethodGet, "http://ts.example.com/login/captcha.png", nil)
	w := httptest.NewRecorder()
	a.captchaImage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q，期望 image/png", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q，验证码图片不该被缓存", cc)
	}
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == captchaCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("没有下发验证码令牌 Cookie")
	}
	if !found.HttpOnly {
		t.Fatal("验证码令牌 Cookie 应为 HttpOnly")
	}
	if found.Path != "/login" {
		t.Fatalf("Cookie Path = %q，期望 /login", found.Path)
	}
	a.captcha.mu.Lock()
	_, ok := a.captcha.items[found.Value]
	a.captcha.mu.Unlock()
	if !ok {
		t.Fatal("令牌应当在服务端有对应记录")
	}
}
