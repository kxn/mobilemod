// Package admin serves an embedded, loopback-only status and SIM number UI.
package admin

import (
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
}
type Backend interface {
	AdminStatus() (Status, error)
	SetModuleNumber(module, sim, number string) error
}
type Handler struct {
	backend    Backend
	host, csrf string
}

func New(backend Backend, host string) *Handler {
	return &Handler{backend: backend, host: host, csrf: rand.Text()}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	_, port, _ := net.SplitHostPort(h.host)
	if r.Host != h.host && r.Host != "localhost:"+port {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
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
	if r.Header.Get("Origin") != "http://"+r.Host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(h.csrf)) != 1 {
		http.Error(w, "Invalid request token", 403)
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
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		http.Error(w, "Invalid request", 400)
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
