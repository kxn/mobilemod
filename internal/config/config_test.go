package config

import (
	"github.com/kxn/mobilemod/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func valid() Config {
	return Config{Database: "test.db", Feishu: Feishu{AppID: "app", AppSecret: "secret", AllowedUsers: []string{"owner"}, Recipients: []domain.Recipient{{Type: "open_id", ID: "owner"}}}, Modules: []Module{{ID: "a", Port: "COM1"}}, AssemblyTimeoutSeconds: 300, MaxParts: 32, SendLimitPerHour: 20, RetentionDays: 30}
}
func TestValidateFailClosed(t *testing.T) {
	tests := []func(*Config){func(c *Config) { c.Feishu.AllowedUsers = nil }, func(c *Config) { c.Modules = append(c.Modules, c.Modules[0]) }, func(c *Config) { c.Modules = append(c.Modules, Module{ID: "b", Port: "com1"}) }, func(c *Config) { c.Feishu.Recipients = nil }, func(c *Config) { c.MaxParts = 999 }, func(c *Config) { c.Modules[0].Forwarding = Forwarding{Backend: "shell"} }, func(c *Config) { c.Modules[0].Number = "+86;whoami" }, func(c *Config) { c.Modules[0].Forwarding = Forwarding{Backend: "sms"} }}
	for i, change := range tests {
		c := valid()
		change(&c)
		if c.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	c := valid()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
}

func TestLANListenAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8765", "192.0.2.10:8765", "[::]:8765", "[::1]:8765"} {
		c := valid()
		c.HealthListen = address
		if err := c.Validate(); err != nil {
			t.Fatal(address, err)
		}
	}

}

func TestWebOnlyCredentialBootstrap(t *testing.T) {
	c := valid()
	c.Feishu.AppID, c.Feishu.AppSecret = "", ""
	if c.Validate() == nil {
		t.Fatal("missing credentials accepted without management UI")
	}
	c.HealthListen = "0.0.0.0:8765"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Feishu.AppID = "app"
	if c.Validate() == nil {
		t.Fatal("partial credentials accepted")
	}
}
func TestReceiverPriority(t *testing.T) {
	for _, c := range [][4]string{{"+12025550100", "+12025550101", "id", "+12025550100"}, {"", "+12025550101", "id", "+12025550101"}, {"", "", "id", "id"}, {"000000", "", "id", "id"}} {
		if got := Receiver(c[0], c[1], c[2]); got != c[3] {
			t.Fatal(got, c)
		}
	}
}
func TestLoadEnvironmentAndStrictJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	raw := `{"database":"state.db","feishu":{"app_id":"a","secret_env":"MOBILEMOD_TEST_SECRET","allowed_users":["owner"],"recipients":[{"type":"open_id","id":"owner"}]},"modules":[{"id":"a","port":"COM1"}]}`
	os.WriteFile(p, []byte(raw), 0600)
	t.Setenv("MOBILEMOD_TEST_SECRET", "private")
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Feishu.AppSecret != "private" || !filepath.IsAbs(c.Database) {
		t.Fatal("configuration defaults/environment")
	}
	for _, bad := range []string{raw + ` {}`, strings.Replace(raw, `"database"`, `"typo"`, 1)} {
		os.WriteFile(p, []byte(bad), 0600)
		if _, e := Load(p); e == nil {
			t.Fatal("accepted invalid config")
		}
	}
}
func FuzzValidNumber(f *testing.F) {
	f.Add("+12025550100")
	f.Add("50000")
	f.Fuzz(func(t *testing.T, s string) {
		if ValidNumber(s) && strings.ContainsAny(s, ";\n\r /\\") {
			t.Fatal("accepted injection")
		}
	})
}

func TestNumberValidationRejectsOuterControlCharacters(t *testing.T) {
	for _, number := range []string{" 50000", "50000 ", "\n50000", "50000\r", "50000\t"} {
		if ValidNumber(number) {
			t.Fatal("untrimmed configuration number accepted")
		}
	}
}
