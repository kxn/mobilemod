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

func TestLANAccessAndCredentialUpdate(t *testing.T) {
	b := &backend{}
	h := New(b)
	request := func(method, path, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://192.0.2.10:8765"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	origin := "http://192.0.2.10:8765"
	if w := request("GET", "/api/status", "", ""); w.Code != 200 {
		t.Fatal("status without login", w.Code)
	}
	if w := request("GET", "/api/auth", "", ""); w.Code != 404 {
		t.Fatal("auth endpoint still exists", w.Code)
	}
	if w := request("PUT", "/api/feishu", `{"app_id":"fictional-app","app_secret":"fictional-secret"}`, "http://evil.example"); w.Code != 403 {
		t.Fatal("cross origin credential update", w.Code)
	}
	if w := request("PUT", "/api/feishu", `{"app_id":"fictional-app","app_secret":"fictional-secret"}`, origin); w.Code != 204 || b.app != "fictional-app" || b.secret != "fictional-secret" {
		t.Fatal("credential update failed", w.Code)
	}
	if w := request("PUT", "/api/modules/one/number", `{"sim_key":"sim","number":"+12025550100"}`, origin); w.Code != 204 || b.number != "+12025550100" {
		t.Fatal(w.Code, b.number)
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
	h := New(b)
	request := func(method, path, body, origin, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
		r.Host = host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	get := request("GET", "/api/status", "", "", "127.0.0.1:8765")
	var view Status
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &view) != nil {
		t.Fatal(get)
	}
	for _, path := range []string{"/", "/assets/app.js", "/assets/style.css"} {
		w := request("GET", path, "", "", "127.0.0.1:8765")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal(path, w.Code)
		}
	}
	body := `{"sim_key":"sim","number":"+12025550100"}`
	for _, c := range []struct{ host, origin string }{{"127.0.0.1:8765", "http://evil.example"}, {"127.0.0.1:8765", ""}} {
		if w := request("PUT", "/api/modules/one/number", body, c.origin, c.host); w.Code != 403 {
			t.Fatal("unsafe write accepted", w.Code)
		}
	}
	if b.calls != 0 {
		t.Fatal("unsafe write reached backend")
	}
	if w := request("PUT", "/api/modules/one/number", body, "http://127.0.0.1:8765", "127.0.0.1:8765"); w.Code != 204 || b.number != "+12025550100" {
		t.Fatal(w.Code, b.number)
	}
	if w := request("PUT", "/api/modules/one/number", `{"sim_key":"old","number":"+12025550101"}`, "http://127.0.0.1:8765", "127.0.0.1:8765"); w.Code != 409 || b.calls != 1 {
		t.Fatal("stale card write accepted", w.Code)
	}
	for _, invalid := range []string{strings.Repeat("x", 5000)} {
		if w := request("PUT", "/api/modules/one/number", invalid, "http://127.0.0.1:8765", "127.0.0.1:8765"); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(http.MethodPost, "/api/status", "", "", "127.0.0.1:8765"); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
