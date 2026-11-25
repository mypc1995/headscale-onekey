package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 图形验证码。
//
// 用途：给暴露在公网上的登录页加一道「人机」门槛，抬高在线爆破的成本。
// 只靠口令限速挡不住「换 IP、慢慢试」的分布式猜测；要求先看图能过一遍，
// 就能把绝大多数脚本挡在门外。
//
// 三条设计取舍：
//  1. 校验放在口令校验**之前**：验证码不对就不跑 PBKDF2，攻击者烧不掉 CPU。
//  2. 一次一用：验证后立刻作废，猜错必须重新取图，避免拿同一个验证码反复枚举。
//  3. 本机请求跳过：来源是回环地址、且 Host 也是回环/本机名时（脚本、本机 e2e、
//     单机部署）不要求验证码。走同机反代的公网请求 Host 是公网域名，仍然要验证码——
//     否则一部署反代，验证码就形同虚设。

const (
	captchaDigits = 4
	captchaTTL    = 5 * time.Minute
	// captchaMax 是同时有效的验证码数量上限。
	// 攻击者可以不停取图把内存撑大，所以要有上限；满了淘汰最老的一条。
	captchaMax = 1024
	// captchaCookie 保存验证码令牌。
	captchaCookie = "hsa_cap"
	captchaWidth  = 160
	captchaHeight = 56
	captchaScale  = 6
)

// captchaEntry 是一条待校验的验证码。
type captchaEntry struct {
	code    string
	expires time.Time
}

// captchaStore 保存尚未使用的验证码。
type captchaStore struct {
	mu    sync.Mutex
	items map[string]captchaEntry
}

func newCaptchaStore() *captchaStore {
	return &captchaStore{items: map[string]captchaEntry{}}
}

// issue 生成一个验证码，返回令牌与答案。
// 取随机数失败时返回空串：宁可让验证码不可用，也不要把整个服务拖垮。
func (s *captchaStore) issue() (token, code string) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", ""
	}
	token = hex.EncodeToString(buf)

	digits := make([]byte, captchaDigits)
	for i := range digits {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", ""
		}
		digits[i] = byte('0' + n.Int64())
	}
	code = string(digits)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.items {
		if now.After(v.expires) {
			delete(s.items, k)
		}
	}
	if len(s.items) >= captchaMax {
		oldestKey := ""
		var oldest time.Time
		for k, v := range s.items {
			if oldestKey == "" || v.expires.Before(oldest) {
				oldestKey, oldest = k, v.expires
			}
		}
		if oldestKey != "" {
			delete(s.items, oldestKey)
		}
	}
	s.items[token] = captchaEntry{code: code, expires: now.Add(captchaTTL)}
	return token, code
}

// verify 校验一次验证码，通过与否都作废这条验证码：同一个令牌不能用第二次。
func (s *captchaStore) verify(token, input string) bool {
	token = strings.TrimSpace(token)
	input = strings.TrimSpace(input)
	if token == "" || input == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[token]
	if !ok {
		return false
	}
	delete(s.items, token)
	if time.Now().After(e.expires) {
		return false
	}
	return strings.EqualFold(e.code, input)
}

// captchaImage 生成验证码图片，并把令牌写进 Cookie。
//
// 每取一次图就换一张新的验证码，旧的那张随即作废。
func (a *App) captchaImage(w http.ResponseWriter, r *http.Request) {
	token, code := a.captcha.issue()
	if token == "" {
		http.Error(w, "captcha unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: captchaCookie, Value: token, Path: "/login",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(captchaTTL / time.Second),
	})
	w.Header().Set("Content-Type", "image/png")
	// 不缓存：页面刷新后拿到的是新验证码，浏览器再用旧图只会让人填错。
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	_, _ = w.Write(captchaPNG(code))
}

// captchaRequired 判断这次登录要不要验证码。
//
// 可信来源（写进 HSA_LOGIN_TRUSTED_IPS 的网段）直接放行——
// 那是运维自己明确声明信任的来源，不该让人对着图输数字。
func (a *App) captchaRequired(r *http.Request) bool {
	ip := clientIP(r)
	if a.login != nil && a.login.trustedSource(ip) {
		return false
	}
	// 本机请求：来源是回环地址，且 Host 也是回环/本机名。
	// 两个条件都要满足：同机反代转过来的公网请求，Host 是公网域名，
	// 照样要验证码。
	if !isLoopbackHost(ip) {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host != "localhost" && !isLoopbackHost(host)
}

// isLoopbackHost 判断字符串是不是回环地址。
func isLoopbackHost(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.IsLoopback()
}

// captchaPNG 把验证码画成 PNG：4 位数字，点阵字模 + 噪点 + 干扰线 + 逐位抖动。
//
// 用 Go 自带的 image/png 就够了，不引第三方字体包：字模写在代码里，
// 依赖越少部署越省事。
func captchaPNG(code string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, captchaWidth, captchaHeight))
	bg := color.RGBA{0xf3, 0xf5, 0xf9, 0xff}
	for y := 0; y < captchaHeight; y++ {
		for x := 0; x < captchaWidth; x++ {
			img.Set(x, y, bg)
		}
	}

	// 噪点
	for i := 0; i < 260; i++ {
		x := randInt(captchaWidth)
		y := randInt(captchaHeight)
		g := uint8(120 + randInt(110))
		img.Set(x, y, color.RGBA{g, g, g, 0xff})
	}

	// 干扰线
	for i := 0; i < 3; i++ {
		y0 := randInt(captchaHeight)
		c := color.RGBA{uint8(80 + randInt(120)), uint8(80 + randInt(120)), uint8(120 + randInt(100)), 0xff}
		for x := 0; x < captchaWidth; x++ {
			y := y0 + randInt(5) - 2
			if y >= 0 && y < captchaHeight {
				img.Set(x, y, c)
			}
		}
	}

	// 调色板：深色系，避免和浅背景混在一起
	palette := []color.RGBA{
		{0x1f, 0x3a, 0x8a, 0xff},
		{0x7a, 0x1f, 0x5c, 0xff},
		{0x0f, 0x60, 0x3a, 0xff},
		{0x8a, 0x2b, 0x1f, 0xff},
		{0x24, 0x24, 0x33, 0xff},
	}
	cell := captchaScale * 5
	gap := 6
	total := len(code)*cell + (len(code)-1)*gap
	x0 := (captchaWidth - total) / 2
	y0 := (captchaHeight - captchaScale*7) / 2

	for i := 0; i < len(code); i++ {
		d := int(code[i] - '0')
		if d < 0 || d > 9 {
			continue
		}
		glyph := captchaFont[d]
		c := palette[randInt(len(palette))]
		// 每个数字独立的横向与纵向抖动，避免固定位置被模板匹配。
		// 横向抖动幅度收窄到 ±1px，免得最左边的数字贴到画布边缘。
		dx := randInt(3) - 1
		dy := randInt(7) - 3
		gx := x0 + i*(cell+gap) + dx
		gy := y0 + dy
		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<uint(4-col)) == 0 {
					continue
				}
				for dy2 := 0; dy2 < captchaScale; dy2++ {
					for dx2 := 0; dx2 < captchaScale; dx2++ {
						x := gx + col*captchaScale + dx2
						y := gy + row*captchaScale + dy2
						if x >= 0 && x < captchaWidth && y >= 0 && y < captchaHeight {
							img.Set(x, y, c)
						}
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// captchaFont 是 5x7 点阵字模（数字 0-9）。
// 每个字节代表一行，低位在左，最高位（第 4 位）是最左列。
var captchaFont = [10][7]byte{
	{0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E}, // 0
	{0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E}, // 1
	{0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F}, // 2
	{0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E}, // 3
	{0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02}, // 4
	{0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E}, // 5
	{0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E}, // 6
	{0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08}, // 7
	{0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E}, // 8
	{0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C}, // 9
}

// randInt 返回 [0, n) 里的随机数。
func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}
