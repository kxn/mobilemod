package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/store"
)

func authFixture(t *testing.T) (*Auth, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	path := filepath.Join(dir, "admin-setup-code")
	a, err := NewAuth(db, path)
	if err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return a, db, string(code)
}

func TestAdminSetupRotationPersistenceAndExpiration(t *testing.T) {
	a, db, code := authFixture(t)
	if setup, logged := a.State(""); !setup || logged {
		t.Fatal("unexpected initial state")
	}
	if _, err := a.Setup("wrong", "fictional-old-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("setup code bypass")
	}
	if _, err := a.Setup(code, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	first, err := a.Setup(code, "fictional-old-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.setupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("initialization code remains")
	}
	if _, err := a.Setup(code, "fictional-old-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("initialization code reused")
	}
	second, err := a.Login("fictional-old-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Change(first, "incorrect", "fictional-new-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password bypass")
	}
	third, err := a.Change(first, "fictional-old-password", "fictional-new-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, logged := a.State(token); logged {
			t.Fatal("old session survived password change")
		}
	}
	if _, logged := a.State(third); !logged {
		t.Fatal("current session lost")
	}
	a.Logout(third)
	if _, logged := a.State(third); logged {
		t.Fatal("logout failed")
	}
	var raw json.RawMessage
	if err := db.View(func(tx *store.Tx) error { return tx.Get("settings", "admin_password", &raw) }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fictional") {
		t.Fatal("plaintext password stored")
	}
	reloaded, err := NewAuth(db, a.setupPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Login("fictional-old-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password accepted")
	}
	token, err := reloaded.Login("fictional-new-password")
	if err != nil {
		t.Fatal(err)
	}
	reloaded.sessions[token] = time.Now().Add(-time.Second)
	if _, logged := reloaded.State(token); logged {
		t.Fatal("expired session accepted")
	}
}

func TestAuthRateLimitAndSetupFileReuse(t *testing.T) {
	a, db, code := authFixture(t)
	reloaded, err := NewAuth(db, a.setupPath)
	if err != nil || reloaded.setupCode != code {
		t.Fatal("restart changed pending initialization")
	}
	for i := 0; i < 10; i++ {
		if _, err := a.Setup("incorrect", "fictional-password"); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if _, err := a.Setup(code, "fictional-password"); !errors.Is(err, ErrRateLimited) {
		t.Fatal("unbounded password attempts")
	}
	a.window = time.Now().Add(-time.Minute)
	if _, err := a.Setup(code, "fictional-password"); err != nil {
		t.Fatal(err)
	}
}
