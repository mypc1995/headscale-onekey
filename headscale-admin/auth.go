package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
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

	_ "modernc.org/sqlite"
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

// AuthStore 用 SQLite 存放管理员账号；数据库同时承载会话。
type AuthStore struct {
	mu sync.Mutex
	DB *sql.DB
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS admins (
  username      TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  salt          TEXT NOT NULL,
  iterations    INTEGER NOT NULL,
  role          TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  disabled      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  username   TEXT NOT NULL,
  role       TEXT NOT NULL,
  csrf       TEXT NOT NULL,
  flash      TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`

// OpenStore 打开（或创建）SQLite 数据库，并把旧的 admins.json 迁移进来。
func OpenStore(dbPath, legacyJSON string) (*AuthStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 写操作串行化，避免 database is locked。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	s := &AuthStore{DB: db}
	if err := s.migrateLegacy(legacyJSON); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(dbPath, 0600)
	return s, nil
}

type legacyAdmins map[string]*Admin

func (s *AuthStore) migrateLegacy(path string) error {
	if path == "" {
		return nil
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	var legacy legacyAdmins
	if err := json.Unmarshal(b, &legacy); err != nil || len(legacy) == 0 {
		return nil
	}
	for _, a := range legacy {
		role := a.Role
		if role != "admin" && role != "readonly" {
			role = "admin"
		}
		iter := a.Iterations
		if iter <= 0 {
			iter = pbkdf2Iterations
		}
		created := a.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		_, err := s.DB.Exec(
			`INSERT OR IGNORE INTO admins (username, password_hash, salt, iterations, role, created_at, disabled) VALUES (?,?,?,?,?,?,?)`,
			a.Username, a.PasswordHash, a.Salt, iter, role, created.Unix(), boolInt(a.Disabled))
		if err != nil {
			return err
		}
	}
	// 迁移完成后保留原文件作为备份，避免重复导入。
	_ = os.Rename(path, path+".migrated")
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
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

func (s *AuthStore) EnsureAdmin(username, password string, lang ...string) error {
	L := reqLang(lang)
	if username == "" || password == "" {
		return errors.New(tr(L, "auth.emptyCred"))
	}
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.DB.Exec(
		`INSERT INTO admins (username, password_hash, salt, iterations, role, created_at, disabled) VALUES (?,?,?,?,?,?,0)
		 ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash, salt=excluded.salt,
		   iterations=excluded.iterations, role='admin', disabled=0`,
		username, h, salt, iter, "admin", time.Now().Unix())
	return err
}

func (s *AuthStore) Exists(username string) bool {
	var one int
	err := s.DB.QueryRow(`SELECT 1 FROM admins WHERE username=?`, username).Scan(&one)
	return err == nil
}

func (s *AuthStore) Verify(username, password string) (*Admin, bool) {
	a := &Admin{Username: username}
	var created int64
	var disabled int
	err := s.DB.QueryRow(
		`SELECT password_hash, salt, iterations, role, created_at, disabled FROM admins WHERE username=?`,
		username).Scan(&a.PasswordHash, &a.Salt, &a.Iterations, &a.Role, &created, &disabled)
	if err != nil {
		return nil, false
	}
	a.CreatedAt = time.Unix(created, 0)
	a.Disabled = disabled != 0
	if a.Disabled || !verifyPassword(a, password) {
		return nil, false
	}
	return &Admin{Username: a.Username, Role: a.Role, Disabled: a.Disabled}, true
}

func (s *AuthStore) List() []*Admin {
	rows, err := s.DB.Query(`SELECT username, role, created_at, disabled FROM admins ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []*Admin{}
	for rows.Next() {
		a := &Admin{}
		var created int64
		var disabled int
		if err := rows.Scan(&a.Username, &a.Role, &created, &disabled); err != nil {
			continue
		}
		a.CreatedAt = time.Unix(created, 0)
		a.Disabled = disabled != 0
		out = append(out, a)
	}
	return out
}

func (s *AuthStore) CreateAdmin(username, password, role string, lang ...string) error {
	L := reqLang(lang)
	if role != "admin" && role != "readonly" {
		role = "readonly"
	}
	if s.Exists(username) {
		return errors.New(tr(L, "auth.userExists"))
	}
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.DB.Exec(
		`INSERT INTO admins (username, password_hash, salt, iterations, role, created_at, disabled) VALUES (?,?,?,?,?,?,0)`,
		username, h, salt, iter, role, time.Now().Unix())
	return err
}

func (s *AuthStore) DeleteAdmin(username string, lang ...string) error {
	L := reqLang(lang)
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return errors.New(tr(L, "auth.lastAdmin"))
	}
	res, err := s.DB.Exec(`DELETE FROM admins WHERE username=?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New(tr(L, "auth.userMissing"))
	}
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE username=?`, username)
	return nil
}

func (s *AuthStore) SetRole(username, role string, lang ...string) error {
	L := reqLang(lang)
	if role != "admin" && role != "readonly" {
		return errors.New(tr(L, "auth.invalidRole"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.DB.Exec(`UPDATE admins SET role=? WHERE username=?`, role, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New(tr(L, "auth.userMissing"))
	}
	return nil
}

func (s *AuthStore) ChangePassword(username, password string, lang ...string) error {
	L := reqLang(lang)
	h, salt, iter, err := newPasswordRecord(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.DB.Exec(`UPDATE admins SET password_hash=?, salt=?, iterations=? WHERE username=?`, h, salt, iter, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New(tr(L, "auth.userMissing"))
	}
	return nil
}

type Session struct {
	Token    string
	Username string
	Role     string
	CSRF     string
	Flash    string
	Expires  time.Time
}

// SessionManager 把会话存进同一个 SQLite，重启后登录状态仍然保留。
type SessionManager struct {
	mu     sync.Mutex
	db     *sql.DB
	secure bool
}

func NewSessionManager(db *sql.DB, secure bool) *SessionManager {
	return &SessionManager{db: db, secure: secure}
}

func (m *SessionManager) Create(username, role string) *Session {
	token, _ := randomHex(32)
	csrf, _ := randomHex(24)
	s := &Session{Token: token, Username: username, Role: role, CSRF: csrf, Expires: time.Now().Add(sessionDuration)}
	_, err := m.db.Exec(
		`INSERT INTO sessions (token, username, role, csrf, flash, expires_at) VALUES (?,?,?,?,'',?)`,
		s.Token, s.Username, s.Role, s.CSRF, s.Expires.Unix())
	if err != nil {
		// 数据库异常时退回内存会话，至少不阻断登录。
		fallbackSession(s)
		return s
	}
	_, _ = m.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Add(-time.Hour).Unix())
	return s
}

// fallbackSession 内存兜底，仅在数据库写入失败时使用。
var fallbackStore sync.Map

func fallbackSession(s *Session) { fallbackStore.Store(s.Token, s) }

func (m *SessionManager) Get(token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	s := &Session{}
	var expires int64
	err := m.db.QueryRow(
		`SELECT token, username, role, csrf, flash, expires_at FROM sessions WHERE token=?`, token).
		Scan(&s.Token, &s.Username, &s.Role, &s.CSRF, &s.Flash, &expires)
	if err != nil {
		if v, ok := fallbackStore.Load(token); ok {
			fs := v.(*Session)
			if time.Now().Before(fs.Expires) {
				return fs, true
			}
			fallbackStore.Delete(token)
		}
		return nil, false
	}
	s.Expires = time.Unix(expires, 0)
	if time.Now().After(s.Expires) {
		m.Delete(token)
		return nil, false
	}
	return s, true
}

func (m *SessionManager) Delete(token string) {
	_, _ = m.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
	fallbackStore.Delete(token)
}

func (m *SessionManager) SetFlash(token, msg string) {
	fallbackStore.Delete(token)
	_, _ = m.db.Exec(`UPDATE sessions SET flash=? WHERE token=?`, msg, token)
}

func (m *SessionManager) TakeFlash(token string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var msg string
	// 单条语句取出并清空，避免两次往返之间出现并发写入。
	if err := m.db.QueryRow(`UPDATE sessions SET flash='' WHERE token=? RETURNING flash`, token).Scan(&msg); err != nil {
		return ""
	}
	return msg
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
	if s == nil {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(s.CSRF)) == 1
}

// Ping 用于启动自检。
func (s *AuthStore) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.DB.PingContext(ctx)
}
