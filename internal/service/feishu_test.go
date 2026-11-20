package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/feishu"
)

func TestFeishuCredentialValidationPersistenceAndRedaction(t *testing.T) {
	cfg := runtimeConfig(t)
	r, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if !strings.Contains(req.URL.Path, "tenant_access_token") {
			t.Error("validation sent a chat message")
		}
		var body struct {
			AppID  string `json:"app_id"`
			Secret string `json:"app_secret"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		if body.Secret != "fictional-valid-secret" {
			io.WriteString(w, `{"code":10003}`)
			return
		}
		io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
	}))
	defer api.Close()
	r.Client.BaseURL = api.URL
	if err := r.SetFeishu(context.Background(), "other-app", "bad"); err == nil {
		t.Fatal("bad credentials saved")
	}
	if r.feishu.current().AppID != cfg.Feishu.AppID {
		t.Fatal("failed validation replaced credentials")
	}
	if err := r.SetFeishu(context.Background(), "other-app", ""); err == nil {
		t.Fatal("old app secret silently reused")
	}
	if err := r.SetFeishu(context.Background(), "other-app", "fictional-valid-secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFeishu(context.Background(), "other-app", ""); err != nil {
		t.Fatal("blank secret did not preserve same app", err)
	}
	status, err := r.AdminStatus()
	if err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(status)
	if !status.FeishuSecretSet || status.FeishuAppID != "other-app" || strings.Contains(string(view), "fictional-valid-secret") {
		t.Fatal("credential status leaked or incorrect")
	}
	if requests.Load() != 3 {
		t.Fatal("unexpected validation requests", requests.Load())
	}
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Store.Close()
	if reloaded.feishu.current().AppID != "other-app" || reloaded.feishu.current().Secret != "fictional-valid-secret" {
		t.Fatal("web credentials did not survive restart")
	}
}

type rotatingListener struct {
	started chan<- string
	stopped chan<- string
	id      string
	ready   atomic.Bool
}

func (l *rotatingListener) IsReady() bool { return l.ready.Load() }
func (l *rotatingListener) Run(ctx context.Context) error {
	l.ready.Store(true)
	l.started <- l.id
	<-ctx.Done()
	l.ready.Store(false)
	l.stopped <- l.id
	return nil
}
func TestManagedFeishuRestartsAndStopsOldListener(t *testing.T) {
	started, stopped := make(chan string, 8), make(chan string, 8)
	m := &managedFeishu{client: feishu.New("old-app", "old-secret"), changed: make(chan struct{}, 1)}
	m.factory = func(c *feishu.Client) EventListener {
		return &rotatingListener{started: started, stopped: stopped, id: c.AppID}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	await := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatal(got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("listener stalled", want)
		}
	}
	await(started, "old-app")
	if !m.IsReady() {
		t.Fatal("listener not ready")
	}
	m.replace(feishu.New("new-app", "new-secret"))
	await(stopped, "old-app")
	await(started, "new-app")
	if m.current().AppID != "new-app" || !m.IsReady() {
		t.Fatal("new listener not current")
	}
	cancel()
	await(stopped, "new-app")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager shutdown stalled")
	}
}

func TestManagedFeishuWaitsForSetupAndRecoversFromBadListener(t *testing.T) {
	started, stopped := make(chan string, 8), make(chan string, 8)
	m := &managedFeishu{client: feishu.New("", ""), changed: make(chan struct{}, 1)}
	m.factory = func(c *feishu.Client) EventListener {
		if c.AppID == "bad-app" {
			started <- c.AppID
			return listenerStub{fail: true}
		}
		return &rotatingListener{started: started, stopped: stopped, id: c.AppID}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	if m.IsReady() {
		t.Fatal("unconfigured listener reported ready")
	}
	m.replace(feishu.New("bad-app", "secret"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bad listener not attempted")
	}
	select {
	case <-done:
		t.Fatal("bad credentials stopped management supervisor")
	default:
	}
	m.replace(feishu.New("fixed-app", "secret"))
	select {
	case id := <-started:
		if id != "fixed-app" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("new credentials did not recover listener")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stalled")
	}
}
