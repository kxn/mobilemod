package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/sms"
	"github.com/kxn/mobilemod/internal/store"
)

type listenerStub struct{ fail bool }

func (s listenerStub) Run(ctx context.Context) error {
	if s.fail {
		return errors.New("listener failed")
	}
	<-ctx.Done()
	return nil
}
func (s listenerStub) IsReady() bool { return !s.fail }
func runtimeConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Database: filepath.Join(t.TempDir(), "state.db"), Feishu: config.Feishu{AppID: "app", AppSecret: "secret", AllowedUsers: []string{"owner"}, Recipients: []domain.Recipient{{Type: "open_id", ID: "owner"}}}, Modules: []config.Module{{ID: "one", Port: "test"}}, AssemblyTimeoutSeconds: 10, MaxParts: 32, SendLimitPerHour: 20, RetentionDays: 30}
}
func TestRuntimeSerialAssemblyToFeishuAndShutdown(t *testing.T) {
	cfg := runtimeConfig(t)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	cfg.HealthListen = ln.Addr().String()
	ln.Close()
	r, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Store.Close()
	r.Listener = listenerStub{}
	notifications := make(chan string, 10)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			io.WriteString(w, `{"code":0,"tenant_access_token":"token","expire":7200}`)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		var content struct {
			Text string `json:"text"`
		}
		json.Unmarshal([]byte(body.Content), &content)
		notifications <- content.Text
		io.WriteString(w, `{"code":0,"data":{"message_id":"forwarded-message","chat_id":"chat"}}`)
	}))
	defer api.Close()
	r.Client.BaseURL = api.URL
	r.Modules[0].Open = func(string) (io.ReadWriteCloser, error) {
		host, device := net.Pipe()
		go func() {
			defer device.Close()
			s := bufio.NewScanner(device)
			offered := false
			for s.Scan() {
				var q map[string]any
				json.Unmarshal(s.Bytes(), &q)
				reply := map[string]any{"id": q["id"]}
				switch q["cmd"] {
				case "status":
					reply["event"] = "status"
					reply["protocol"] = 2
					reply["device_id"] = "physical"
					reply["iccid"] = "sim"
					reply["number"] = "+12025550100"
					reply["core"] = "V2044"
					reply["network"] = 1
					reply["sms_ready_event_seen"] = true
				case "inbox":
					reply["event"] = "inbox"
					if !offered {
						offered = true
						reply["messages"] = []domain.Envelope{{Event: "sms_received", BootID: "b", MessageID: 1, From: "50000", Text: "第二片", ICCID: "sim", Durable: true, Metadata: domain.Metadata{Ref: 4, Total: 2, Sequence: 2}}, {Event: "sms_received", BootID: "b", MessageID: 2, From: "50000", Text: "第一片", ICCID: "sim", Durable: true, Metadata: domain.Metadata{Ref: 4, Total: 2, Sequence: 1}}}
					}
				case "ack":
					reply["event"] = "ack"
				}
				b, _ := json.Marshal(reply)
				if _, e := device.Write(append(b, '\n')); e != nil {
					return
				}
			}
		}()
		return host, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer cancel()
	select {
	case text := <-notifications:
		for _, expected := range []string{"第一片第二片", "+12025550100", "one", "50000"} {
			if !strings.Contains(text, expected) {
				t.Fatal(text)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end-to-end notification")
	}
	healthClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	defer healthClient.CloseIdleConnections()
	for _, endpoint := range []string{"/healthz", "/readyz", "/api/status", "/"} {
		res, e := healthClient.Get("http://" + cfg.HealthListen + endpoint)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(7 * time.Second):
		buffer := make([]byte, 64<<10)
		n := runtime.Stack(buffer, true)
		t.Fatalf("shutdown blocked:\n%s", buffer[:n])
	}
}
func TestRuntimePropagatesListenerFailureAndLocalSendOffline(t *testing.T) {
	r, e := New(runtimeConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Store.Close()
	r.Listener = listenerStub{fail: true}
	r.Modules[0].Open = func(string) (io.ReadWriteCloser, error) { return nil, errors.New("offline") }
	if e = r.Run(context.Background()); e == nil {
		t.Fatal("lost listener failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = r.LocalSend(ctx, "one", "50000", "101", "id"); e == nil {
		t.Fatal("send with cancelled context")
	}
	if _, e = r.LocalSend(ctx, "missing", "50000", "101", "id"); e == nil {
		t.Fatal("unknown module")
	}
}
func TestRetentionPreservesPendingNotificationsAndGroups(t *testing.T) {
	e, _ := engine(t)
	old := time.Now().Add(-40 * 24 * time.Hour)
	now := time.Now()
	e.Store.Update(func(tx *store.Tx) error {
		for _, r := range []struct {
			bucket, key string
			v           any
		}{{"raw", "old", sms.Raw{At: old}}, {"messages", "old", domain.Message{ReceivedAt: old}}, {"outbox", "sent", domain.Outbox{CreatedAt: old, SentAt: &old}}, {"outbox", "pending", domain.Outbox{CreatedAt: old}}, {"commands", "done", domain.CommandJob{CreatedAt: old, State: "done"}}, {"commands", "pending", domain.CommandJob{CreatedAt: old, State: "pending"}}, {"sends", "old", domain.SendRecord{CreatedAt: old}}, {"groups", "done", sms.Group{Started: old, Closed: true}}, {"groups", "pending", sms.Group{Started: old}}} {
			if err := tx.Put(r.bucket, r.key, r.v); err != nil {
				return err
			}
		}
		return nil
	})
	if err := e.Cleanup(now); err != nil {
		t.Fatal(err)
	}
	e.Store.View(func(tx *store.Tx) error {
		for _, bucket := range []string{"outbox", "commands", "groups"} {
			if tx.Count(bucket) != 1 || !tx.Has(bucket, "pending") {
				t.Fatal("lost pending", bucket)
			}
		}
		for _, bucket := range []string{"raw", "messages", "sends"} {
			if tx.Count(bucket) != 0 {
				t.Fatal("retention failed", bucket)
			}
		}
		return nil
	})
}
