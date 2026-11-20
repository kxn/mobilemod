package admin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/store"
)

var (
	ErrUnauthorized = errors.New("authentication failed")
	ErrRateLimited  = errors.New("too many attempts; try again in one minute")
)

const sessionCookie = "mobilemod_session"
const sessionLifetime = 12 * time.Hour

type passwordRecord struct {
	Salt []byte `json:"salt"`
	Hash []byte `json:"hash"`
}

// Auth persists only a salted password hash. Sessions expire and are revoked
// together on password changes; the first-use code is an owner-only file.
type Auth struct {
	mu                   sync.Mutex
	db                   *store.Store
	password             passwordRecord
	setupCode, setupPath string
	sessions             map[string]time.Time
	window               time.Time
	attempts             int
}

func NewAuth(db *store.Store, setupPath string) (*Auth, error) {
	a := &Auth{db: db, setupPath: setupPath, sessions: make(map[string]time.Time)}
	err := db.View(func(tx *store.Tx) error { return tx.Get("settings", "admin_password", &a.password) })
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if len(a.password.Hash) != 32 || len(a.password.Salt) != 32 {
			return nil, errors.New("invalid stored admin password")
		}
		return a, nil
	}
	if info, err := os.Lstat(setupPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("admin setup file must be a regular file")
		}
		b, err := os.ReadFile(setupPath)
		if err != nil {
			return nil, err
		}
		if len(b) < 24 || len(b) > 128 {
			return nil, errors.New("invalid admin setup file")
		}
		if err := os.Chmod(setupPath, 0600); err != nil {
			return nil, err
		}
		a.setupCode = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		a.setupCode = rand.Text()
		f, err := os.OpenFile(setupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.WriteString(a.setupCode)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *Auth) State(token string) (setup, authenticated bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	deadline, ok := a.sessions[token]
	if ok && !time.Now().Before(deadline) {
		delete(a.sessions, token)
		ok = false
	}
	return len(a.password.Hash) == 0, ok && token != ""
}

func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 256 }
func passwordHash(password string, salt []byte) []byte {
	hash, _ := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	return hash
}
func equalText(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func (a *Auth) limit() error {
	if time.Since(a.window) >= time.Minute {
		a.window, a.attempts = time.Now(), 0
	}
	if a.attempts >= 10 {
		return ErrRateLimited
	}
	a.attempts++
	return nil
}
func (a *Auth) session() string {
	now := time.Now()
	for token, deadline := range a.sessions {
		if !now.Before(deadline) {
			delete(a.sessions, token)
		}
	}
	if len(a.sessions) >= 128 {
		var oldest string
		var deadline time.Time
		for token, at := range a.sessions {
			if oldest == "" || at.Before(deadline) {
				oldest, deadline = token, at
			}
		}
		delete(a.sessions, oldest)
	}
	token := rand.Text()
	a.sessions[token] = now.Add(sessionLifetime)
	return token
}
func (a *Auth) savePassword(password string) error {
	if !validPassword(password) {
		return errors.New("管理口令须为 12 至 256 字节")
	}
	record := passwordRecord{Salt: make([]byte, 32)}
	if _, err := rand.Read(record.Salt); err != nil {
		return err
	}
	record.Hash = passwordHash(password, record.Salt)
	if err := a.db.Update(func(tx *store.Tx) error { return tx.Put("settings", "admin_password", record) }); err != nil {
		return err
	}
	a.password = record
	a.sessions = make(map[string]time.Time)
	return nil
}
func (a *Auth) Setup(code, password string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.limit(); err != nil {
		return "", err
	}
	if len(a.password.Hash) != 0 || a.setupCode == "" || !equalText(code, a.setupCode) {
		return "", ErrUnauthorized
	}
	if err := a.savePassword(password); err != nil {
		return "", err
	}
	a.setupCode = ""
	// The code is already unusable even if filesystem cleanup fails.
	_ = os.Remove(a.setupPath)
	return a.session(), nil
}
func (a *Auth) Login(password string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.limit(); err != nil {
		return "", err
	}
	if len(password) > 256 || len(a.password.Hash) == 0 || subtle.ConstantTimeCompare(passwordHash(password, a.password.Salt), a.password.Hash) != 1 {
		return "", ErrUnauthorized
	}
	return a.session(), nil
}
func (a *Auth) Change(token, old, password string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.limit(); err != nil {
		return "", err
	}
	if deadline, ok := a.sessions[token]; !ok || !time.Now().Before(deadline) {
		return "", ErrUnauthorized
	}
	if len(old) > 256 || subtle.ConstantTimeCompare(passwordHash(old, a.password.Salt), a.password.Hash) != 1 {
		return "", ErrUnauthorized
	}
	if err := a.savePassword(password); err != nil {
		return "", err
	}
	return a.session(), nil
}
func (a *Auth) Logout(token string) { a.mu.Lock(); defer a.mu.Unlock(); delete(a.sessions, token) }
