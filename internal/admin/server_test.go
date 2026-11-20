package admin

import (
	"context"
	"encoding/json"
	"github.com/kxn/mobilemod/internal/modem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type backend struct {
	number      string
	calls       int
	app, secret string
}

func (b *backend) SetFeishu(_ context.Context, app, secret string) error {
	b.app, b.secret = app, secret
	return nil
}

func (b *backend) AdminStatus() (Status, error) {
	return Status{Feishu: true, Modules: []modem.Snapshot{{ID: "one", SIMKey: "sim", Online: true, Receiver: b.number}}}, nil
}

func TestLANAuthenticationAndCredentialProtection(t *testing.T) {
	a, _, code := authFixture(t)
	b := &backend{}
	h := New(b, "[::]:8765")
	h.Auth = a
	var cookie *http.Cookie
	request := func(method, path, body, origin string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://192.0.2.10:8765"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", h.csrf)
		if authenticated && cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	origin := "http://192.0.2.10:8765"
	for _, path := range []string{"/api/status", "/api/modules/one/number", "/api/feishu"} {
		method := "PUT"
		if path == "/api/status" {
			method = "GET"
		}
		if w := request(method, path, `{}`, origin, false); w.Code != 401 {
			t.Fatal("anonymous API access", path, w.Code)
		}
	}
	if w := request("GET", "/api/auth", "", "", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"setup_required":true`) {
		t.Fatal(w)
	}
	setup, _ := json.Marshal(map[string]string{"setup_code": code, "password": "fictional-admin-password"})
	if w := request("POST", "/api/auth/setup", string(setup), "http://other.example", false); w.Code != 403 {
		t.Fatal("cross origin setup", w.Code)
	}
	w := request("POST", "/api/auth/setup", string(setup), origin, false)
	if w.Code != 204 || len(w.Result().Cookies()) != 1 {
		t.Fatal("setup failed", w.Code)
	}
	cookie = w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatal("unsafe session cookie")
	}
	if w := request("GET", "/api/status", "", "", true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("PUT", "/api/feishu", `{"app_id":"fictional-app","app_secret":"fictional-secret"}`, origin, true); w.Code != 204 || b.app != "fictional-app" || b.secret != "fictional-secret" {
		t.Fatal("authenticated credential update failed", w.Code)
	}
	if w := request("PUT", "/api/feishu", `{"app_id":"fictional-app","app_secret":"fictional-secret","unknown":true}`, origin, true); w.Code != 400 {
		t.Fatal("loose credential schema")
	}
	if w := request("POST", "/api/auth/logout", `{}`, origin, true); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/status", "", "", true); w.Code != 401 {
		t.Fatal("logged out session accepted")
	}
	if h.validHost("malicious.example:8765") || h.validHost("192.0.2.10:9999") {
		t.Fatal("arbitrary DNS host or port accepted")
	}
	if !h.validHost("[2001:db8::1]:8765") {
		t.Fatal("IPv6 LAN host rejected")
	}
}
func (b *backend) SetModuleNumber(id, sim, number string) error {
	if id != "one" || sim != "sim" {
		return modem.ErrSIMChanged
	}
	b.number = number
	b.calls++
	return nil
}
func TestUIAndWriteProtection(t *testing.T) {
	b := &backend{}
	h := New(b, "127.0.0.1:8765")
	request := func(method, path, body, origin, token, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
		r.Host = host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	get := request("GET", "/api/status", "", "", "", "127.0.0.1:8765")
	var view Status
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &view) != nil || view.CSRF == "" {
		t.Fatal(get)
	}
	for _, path := range []string{"/", "/assets/app.js", "/assets/style.css"} {
		w := request("GET", path, "", "", "", "127.0.0.1:8765")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal(path, w.Code)
		}
	}
	body := `{"sim_key":"sim","number":"+12025550100"}`
	for _, c := range []struct{ host, origin, token string }{{"evil.example:8765", "http://evil.example:8765", view.CSRF}, {"127.0.0.1:8765", "http://evil.example", view.CSRF}, {"127.0.0.1:8765", "", view.CSRF}, {"127.0.0.1:8765", "http://127.0.0.1:8765", "wrong"}} {
		if w := request("PUT", "/api/modules/one/number", body, c.origin, c.token, c.host); w.Code != 403 {
			t.Fatal("unsafe write accepted", w.Code)
		}
	}
	if b.calls != 0 {
		t.Fatal("unsafe write reached backend")
	}
	if w := request("PUT", "/api/modules/one/number", body, "http://127.0.0.1:8765", view.CSRF, "127.0.0.1:8765"); w.Code != 204 || b.number != "+12025550100" {
		t.Fatal(w.Code, b.number)
	}
	if w := request("PUT", "/api/modules/one/number", `{"sim_key":"old","number":"+12025550101"}`, "http://127.0.0.1:8765", view.CSRF, "127.0.0.1:8765"); w.Code != 409 || b.calls != 1 {
		t.Fatal("stale card write accepted", w.Code)
	}
	for _, invalid := range []string{`{"sim_key":"sim","number":"x","secret":"x"}`, body + ` {}`, strings.Repeat("x", 5000)} {
		if w := request("PUT", "/api/modules/one/number", invalid, "http://127.0.0.1:8765", view.CSRF, "127.0.0.1:8765"); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(http.MethodPost, "/api/status", "", "", "", "127.0.0.1:8765"); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
