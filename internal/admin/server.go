// Package admin serves the authenticated status and configuration UI.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/kxn/mobilemod/internal/modem"
)

//go:embed assets/*
var assets embed.FS

type Status struct {
	Feishu               bool             `json:"feishu"`
	Modules              []modem.Snapshot `json:"modules"`
	PendingNotifications int              `json:"pending_notifications"`
	CSRF                 string           `json:"csrf"`
	FeishuAppID          string           `json:"feishu_app_id"`
	FeishuSecretSet      bool             `json:"feishu_secret_set"`
}
type Backend interface {
	AdminStatus() (Status, error)
	SetModuleNumber(module, sim, number string) error
}
type Handler struct {
	backend    Backend
	host, csrf string
	Auth       *Auth
}

func New(backend Backend, host string) *Handler {
	return &Handler{backend: backend, host: host, csrf: rand.Text()}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if !h.validHost(r.Host) {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != requestOrigin(r) {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
	}
	if h.Auth != nil && (r.URL.Path == "/api/auth" || strings.HasPrefix(r.URL.Path, "/api/auth/")) {
		h.serveAuth(w, r)
		return
	}
	if h.Auth != nil && r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
		_, authenticated := h.Auth.State(sessionToken(r))
		if !authenticated {
			http.Error(w, "请先登录管理界面", http.StatusUnauthorized)
			return
		}
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/api/status":
			s, err := h.backend.AdminStatus()
			if err != nil {
				http.Error(w, "Status unavailable", 503)
				return
			}
			s.CSRF = h.csrf
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(s)
		case "/", "/assets/app.js", "/assets/style.css":
			path := "assets/index.html"
			contentType := "text/html; charset=utf-8"
			if r.URL.Path == "/assets/app.js" {
				path = "assets/app.js"
				contentType = "text/javascript; charset=utf-8"
			}
			if r.URL.Path == "/assets/style.css" {
				path = "assets/style.css"
				contentType = "text/css; charset=utf-8"
			}
			data, err := assets.ReadFile(path)
			if err != nil {
				http.Error(w, "UI unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !h.validMutation(r) {
		http.Error(w, "Invalid request token", 403)
		return
	}
	if r.URL.Path == "/api/feishu" {
		backend, ok := h.backend.(interface {
			SetFeishu(context.Context, string, string) error
		})
		if !ok {
			http.Error(w, "配置接口不可用", 503)
			return
		}
		var body struct {
			AppID  string `json:"app_id"`
			Secret string `json:"app_secret"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := backend.SetFeishu(r.Context(), body.AppID, body.Secret); err != nil {
			http.Error(w, "飞书认证校验或保存失败；原配置仍然生效", 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "JSON required", 415)
		return
	}
	const prefix = "/api/modules/"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, "/number") {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/number")
	if _, err := url.PathUnescape(id); err != nil || id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		SIM    string `json:"sim_key"`
		Number string `json:"number"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	err := h.backend.SetModuleNumber(id, body.SIM, strings.TrimSpace(body.Number))
	if errors.Is(err, modem.ErrSIMChanged) {
		http.Error(w, "SIM已变化或不可用，请刷新后重新设置", 409)
		return
	}
	if err != nil {
		http.Error(w, "号码无效或模块不可用", 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func (h *Handler) validHost(host string) bool {
	bind, port, err := net.SplitHostPort(h.host)
	if err != nil {
		return false
	}
	name, requestPort, err := net.SplitHostPort(host)
	if err != nil || requestPort != port {
		return false
	}
	ip := net.ParseIP(bind)
	if ip != nil && ip.IsUnspecified() {
		return net.ParseIP(name) != nil || name == "localhost"
	}
	return host == h.host || (ip != nil && ip.IsLoopback() && name == "localhost")
}
func (h *Handler) validMutation(r *http.Request) bool {
	return r.Header.Get("Origin") == requestOrigin(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(h.csrf)) == 1
}
func decodeBody(w http.ResponseWriter, r *http.Request, body any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "JSON required", 415)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(body); err != nil {
		http.Error(w, "Invalid request", 400)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		http.Error(w, "Invalid request", 400)
		return false
	}
	return true
}
func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}
func setSession(w http.ResponseWriter, r *http.Request, token string) {
	age := int(sessionLifetime.Seconds())
	if token == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (h *Handler) serveAuth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/auth" && r.Method == http.MethodGet {
		setup, authenticated := h.Auth.State(sessionToken(r))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"setup_required": setup, "authenticated": authenticated, "csrf": h.csrf})
		return
	}
	if (r.Method != http.MethodPost && r.Method != http.MethodPut) || !h.validMutation(r) {
		http.Error(w, "Invalid request token or method", 403)
		return
	}
	var body struct {
		Password string `json:"password"`
		Old      string `json:"old_password"`
		Code     string `json:"setup_code"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	var token string
	var err error
	switch {
	case r.URL.Path == "/api/auth/setup" && r.Method == http.MethodPost:
		token, err = h.Auth.Setup(body.Code, body.Password)
	case r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost:
		token, err = h.Auth.Login(body.Password)
	case r.URL.Path == "/api/auth/password" && r.Method == http.MethodPut:
		token, err = h.Auth.Change(sessionToken(r), body.Old, body.Password)
	case r.URL.Path == "/api/auth/logout" && r.Method == http.MethodPost:
		h.Auth.Logout(sessionToken(r))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUnauthorized) {
			status = http.StatusUnauthorized
		}
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "60")
		}
		http.Error(w, "认证失败、口令长度不符或尝试过于频繁", status)
		return
	}
	setSession(w, r, token)
	w.WriteHeader(http.StatusNoContent)
}
