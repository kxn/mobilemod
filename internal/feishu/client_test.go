package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kxn/mobilemod/internal/domain"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenCachingUUIDAndAPIFailure(t *testing.T) {
	var tokens, messages atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			tokens.Add(1)
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("receive_id_type") != "open_id" {
			t.Error("bad auth/recipient")
		}
		var q map[string]string
		json.NewDecoder(r.Body).Decode(&q)
		if q["uuid"] != "stable" {
			t.Error("unstable uuid")
		}
		messages.Add(1)
		if fail.Load() {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"code":99991672,"msg":"secret-do-not-log"}`)
		} else {
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		}
	}))
	defer server.Close()
	c := New("app", "top-secret")
	c.BaseURL = server.URL
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := c.SendText(context.Background(), domain.Recipient{Type: "open_id", ID: "owner"}, "hello", "stable"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if tokens.Load() != 1 || messages.Load() != 8 {
		t.Fatal(tokens.Load(), messages.Load())
	}
	fail.Store(true)
	e := c.SendText(context.Background(), domain.Recipient{Type: "open_id", ID: "owner"}, "hello", "stable")
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
}
func TestMalformedAndRedirectResponses(t *testing.T) {
	for _, body := range []string{`not json`, `{"code":0,"expire":0}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c := New("app", "secret")
		c.BaseURL = s.URL
		if e := c.SendText(context.Background(), domain.Recipient{Type: "open_id", ID: "o"}, "x", "x"); e == nil {
			t.Fatal("accepted", body)
		}
		s.Close()
	}
}
func ptr(s string) *string { return &s }
func TestNormalizeUserTextOnly(t *testing.T) {
	e := &larkim.P2MessageReceiveV1{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "ev"}}, Event: &larkim.P2MessageReceiveV1Data{Message: &larkim.EventMessage{MessageId: ptr("message"), ChatId: ptr("chat"), ChatType: ptr("p2p"), MessageType: ptr("text"), CreateTime: ptr(fmt.Sprint(time.Now().UnixMilli())), Content: ptr(`{"text":"/help"}`)}, Sender: &larkim.EventSender{SenderType: ptr("user"), SenderId: &larkim.UserId{OpenId: ptr("owner")}}}}
	in, ok := Normalize(e)
	if !ok || in.EventID != "ev" || in.Text != "/help" {
		t.Fatal(in, ok)
	}
	e.Event.Message.ParentId = ptr("quoted")
	e.Event.Message.Content = ptr(`{"text":"  reply\ntrailing  "}`)
	if in, ok = Normalize(e); !ok || in.ParentID != "quoted" || in.Text != "  reply\ntrailing  " {
		t.Fatal("reply metadata/whitespace lost", in)
	}
	e.Event.Sender.SenderType = ptr("app")
	if _, ok = Normalize(e); ok {
		t.Fatal("accepted bot echo")
	}
	if _, ok = Normalize(nil); ok {
		t.Fatal("accepted nil")
	}
}
