package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	pbkdf2Iterations = 120000
	pbkdf2KeyLen     = 32
	sessionCookie    = "hsa_session"
	sessionDuration  = 8 * time.Hour
)

type sessionKeyType struct{}

var sessionKey sessionKeyType

type Admin struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Salt         string    `json:"salt"`
	Iterations   int       `json:"iterations"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	Disabled     bool      `json:"disabled"`
}

type AuthStore struct {
	mu     sync.RWMutex
	Path   string
	Admins map[string]*Admin
}

func LoadAuthStore(path string) (*AuthStore, error) {
	s := &AuthStore{Path: path, Admins: map[string]*Admin{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.Admins); err != nil {
		return nil, fmt.Errorf("解析管理员文件失败: %w", err)
	}
	if s.Admins == nil {
		s.Admins = map[string]*Admin{}
	}
	return s, nil
}

func (s *AuthStore) saveLocked() error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.Admins, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".admins-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	h := sha256.New
	hashLen := h().Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	var dk []byte
	for i := 1; i <= blocks; i++ {
		prf := hmac.New(h, password)
		prf.Write(salt)
		_ = binary.Write(prf, binary.BigEndian, uint32(i))
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for j := 1; j < iter; j++ {
			prf = hmac.New(h, password)
			prf.Write(u)
			u = prf.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newPasswordRecord(password string) (hashHex, saltHex string, iterations int, err error) {
	saltHex, err = randomHex(16)
	if err != nil {
		return "", "", 0, err
	}
	salt, _ := hex.DecodeString(saltHex)
	key := pbkdf2Key([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return hex.EncodeToString(key), saltHex, pbkdf2Iterations, nil
}

func verifyPassword(a *Admin, password string) bool {
	salt, err := hex.DecodeString(a.Salt)
	if err != nil {
		return false
	}
	iter := a.Iterations
	if iter <= 0 {
		iter = pbkdf2Iterations
	}
	got := pbkdf2Key([]byte(password), salt, iter, pbkdf2KeyLen)
	want, err := hex.DecodeString(a.PasswordHash)
	if err != nil || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *AuthStore) EnsureAdmin(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if username == "" || password == "" {
		return errors.New("用户名或密码为空")
	}
	if a, ok := s.Admins[username]; ok {
		a.Disabled = false
		a.Role = "admin"
	}
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	s.Admins[username] = &Admin{Username: username, PasswordHash: h, Salt: salt, Iterations: iter, Role: "admin", CreatedAt: time.Now()}
	return s.saveLocked()
}

func (s *AuthStore) Exists(username string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Admins[username]
	return ok
}

func (s *AuthStore) Verify(username, password string) (*Admin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.Admins[username]
	if !ok || a.Disabled || !verifyPassword(a, password) {
		return nil, false
	}
	return &Admin{Username: a.Username, Role: a.Role, Disabled: a.Disabled}, true
}

func (s *AuthStore) List() []*Admin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Admin, 0, len(s.Admins))
	for _, a := range s.Admins {
		out = append(out, &Admin{Username: a.Username, Role: a.Role, CreatedAt: a.CreatedAt, Disabled: a.Disabled})
	}
	return out
}

func (s *AuthStore) CreateAdmin(username, password, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Admins[username]; ok {
		return errors.New("用户已存在")
	}
	if role != "admin" && role != "readonly" {
		role = "readonly"
	}
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	s.Admins[username] = &Admin{Username: username, PasswordHash: h, Salt: salt, Iterations: iter, Role: role, CreatedAt: time.Now()}
	return s.saveLocked()
}

func (s *AuthStore) DeleteAdmin(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Admins) <= 1 {
		return errors.New("不能删除最后一个管理员")
	}
	if _, ok := s.Admins[username]; !ok {
		return errors.New("用户不存在")
	}
	delete(s.Admins, username)
	return s.saveLocked()
}

func (s *AuthStore) SetRole(username, role string) error {
	if role != "admin" && role != "readonly" {
		return errors.New("无效角色")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Admins[username]
	if !ok {
		return errors.New("用户不存在")
	}
	a.Role = role
	return s.saveLocked()
}

func (s *AuthStore) ChangePassword(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Admins[username]
	if !ok {
		return errors.New("用户不存在")
	}
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	a.PasswordHash, a.Salt, a.Iterations = h, salt, iter
	return s.saveLocked()
}

type Session struct {
	Token    string
	Username string
	Role     string
	CSRF     string
	Flash    string
	Expires  time.Time
}

type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	secure   bool
}

func NewSessionManager(secure bool) *SessionManager {
	return &SessionManager{sessions: map[string]*Session{}, secure: secure}
}

func (m *SessionManager) Create(username, role string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, _ := randomHex(32)
	csrf, _ := randomHex(24)
	s := &Session{Token: token, Username: username, Role: role, CSRF: csrf, Expires: time.Now().Add(sessionDuration)}
	m.sessions[token] = s
	return s
}

func (m *SessionManager) Get(token string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.Expires) {
		delete(m.sessions, token)
		return nil, false
	}
	return s, true
}

func (m *SessionManager) Delete(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

func (m *SessionManager) SetFlash(token, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[token]; ok {
		s.Flash = msg
	}
}

func (m *SessionManager) TakeFlash(token string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[token]; ok {
		msg := s.Flash
		s.Flash = ""
		return msg
	}
	return ""
}

func (m *SessionManager) SetCookie(w http.ResponseWriter, s *Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  s.Expires,
	})
}

func (m *SessionManager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
}

func (m *SessionManager) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			if s, ok := m.Get(c.Value); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, s)))
				return
			}
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func sessionFrom(r *http.Request) *Session {
	s, _ := r.Context().Value(sessionKey).(*Session)
	return s
}

func validCSRF(r *http.Request, s *Session) bool {
	if err := r.ParseForm(); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(s.CSRF)) == 1
}
