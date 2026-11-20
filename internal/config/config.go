package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/phone"
)

type Forwarding struct {
	Backend       string            `json:"backend"`
	ServiceNumber string            `json:"service_number,omitempty"`
	Query         string            `json:"query,omitempty"`
	Set           map[string]string `json:"set,omitempty"`
	Cancel        map[string]string `json:"cancel,omitempty"`
}
type Module struct {
	ID             string     `json:"id"`
	Port           string     `json:"port"`
	Number         string     `json:"number,omitempty"`
	ExpectedDevice string     `json:"expected_device_id,omitempty"`
	Forwarding     Forwarding `json:"forwarding"`
}
type Feishu struct {
	AppID        string             `json:"app_id"`
	AppSecret    string             `json:"app_secret,omitempty"`
	SecretEnv    string             `json:"secret_env,omitempty"`
	AllowedUsers []string           `json:"allowed_users"`
	AllowedChats []string           `json:"allowed_chats,omitempty"`
	Recipients   []domain.Recipient `json:"recipients"`
}
type Config struct {
	Database               string   `json:"database"`
	Feishu                 Feishu   `json:"feishu"`
	Modules                []Module `json:"modules"`
	AssemblyTimeoutSeconds int      `json:"assembly_timeout_seconds"`
	MaxParts               int      `json:"max_parts"`
	SendLimitPerHour       int      `json:"send_limit_per_hour"`
	AllowedDestinations    []string `json:"allowed_destinations,omitempty"`
	RetentionDays          int      `json:"retention_days"`
	HealthListen           string   `json:"health_listen,omitempty"`
}

func Load(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid config: %w", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	if c.Feishu.SecretEnv != "" {
		c.Feishu.AppSecret = os.Getenv(c.Feishu.SecretEnv)
	}
	if c.Database == "" {
		c.Database = "data/mobilemod.db"
	}
	if !filepath.IsAbs(c.Database) {
		c.Database = filepath.Join(filepath.Dir(path), c.Database)
	}
	if c.AssemblyTimeoutSeconds == 0 {
		c.AssemblyTimeoutSeconds = 300
	}
	if c.MaxParts == 0 {
		c.MaxParts = 32
	}
	if c.SendLimitPerHour == 0 {
		c.SendLimitPerHour = 20
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Feishu.AppID == "" || c.Feishu.AppSecret == "" {
		if c.HealthListen == "" || c.Feishu.AppID != "" || c.Feishu.AppSecret != "" {
			return errors.New("Feishu app_id and secret must be set together, or both omitted when Web management is enabled")
		}
	}
	if len(c.Feishu.AllowedUsers) == 0 {
		return errors.New("allowed_users must not be empty (fail closed)")
	}
	if len(c.Feishu.Recipients) == 0 {
		return errors.New("at least one notification recipient is required")
	}
	for _, r := range c.Feishu.Recipients {
		if (r.Type != "open_id" && r.Type != "chat_id") || r.ID == "" {
			return errors.New("invalid notification recipient")
		}
	}
	if len(c.Modules) == 0 {
		return errors.New("at least one module is required")
	}
	ids, ports := map[string]bool{}, map[string]bool{}
	for _, m := range c.Modules {
		if m.ID == "" || strings.ContainsAny(m.ID, " /\\\r\n\t") || m.Port == "" {
			return errors.New("module id and explicit serial port are required")
		}
		p := strings.ToLower(m.Port)
		if ids[m.ID] || ports[p] {
			return errors.New("duplicate module id or serial port")
		}
		ids[m.ID] = true
		ports[p] = true
		if m.Number != "" && !ValidNumber(m.Number) {
			return fmt.Errorf("invalid receiving number for module %s", m.ID)
		}
		b := m.Forwarding.Backend
		if b != "" && b != "unsupported" && b != "sms" {
			return fmt.Errorf("unknown forwarding backend for %s", m.ID)
		}
		if b == "sms" && !ValidNumber(m.Forwarding.ServiceNumber) {
			return errors.New("SMS forwarding backend requires service_number")
		}
	}
	if c.AssemblyTimeoutSeconds < 10 || c.AssemblyTimeoutSeconds > 86400 || c.MaxParts < 2 || c.MaxParts > 128 || c.SendLimitPerHour < 1 || c.SendLimitPerHour > 1000 || c.RetentionDays < 1 || c.RetentionDays > 3650 {
		return errors.New("limits outside supported range")
	}
	for _, n := range c.AllowedDestinations {
		if !ValidNumber(n) {
			return errors.New("invalid allowed destination")
		}
	}
	if c.HealthListen != "" {
		host, port, err := net.SplitHostPort(c.HealthListen)
		if err != nil || net.ParseIP(host) == nil || port == "" {
			return errors.New("health_listen requires a literal IP and port, for example 0.0.0.0:8765")
		}
		if _, err := net.LookupPort("tcp", port); err != nil {
			return errors.New("invalid health listener port")
		}
	}
	return nil
}
func ValidNumber(s string) bool {
	return s == strings.TrimSpace(s) && phone.Normalize(s) != ""
}
func Receiver(actual, manual, id string) string {
	if n := phone.Normalize(actual); n != "" {
		return n
	}
	if n := phone.Normalize(manual); n != "" {
		return n
	}
	return id
}
