package service

import (
	"context"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/notify"
	"github.com/kxn/mobilemod/internal/store"
	"strings"
	"testing"
	"time"
)

func TestSourceRoutingWhitespaceAndMissingOrDuplicateSIM(t *testing.T) {
	e, d := engine(t)
	ctx := context.Background()
	body := "  第一行  多空格\n第二行\t尾部  "
	e.Accept(ctx, incoming("payload", "/sms +12025550100 +12025550101 "+body))
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if d.sends != 1 || d.texts[0] != body || d.targets[0] != "+12025550101" {
		t.Fatal(d.texts, d.targets)
	}
	d.mu.Lock()
	d.state = testState("one", "+12025550102", "new-card")
	d.mu.Unlock()
	e.Accept(ctx, incoming("missing", "/sms +12025550100 +12025550101 text"))
	e.Tick(ctx)
	if d.sends != 1 {
		t.Fatal("new SIM used for old source")
	}
	other := &fakeDevice{state: testState("two", "+12025550102", "other-card")}
	e.Modules["two"] = other
	e.Accept(ctx, incoming("duplicate", "/sms +12025550102 +12025550101 text"))
	e.Tick(ctx)
	if d.sends != 1 || other.sends != 0 {
		t.Fatal("ambiguous source selected")
	}
}

func TestReplyFollowsNumberToNewModuleAndChecksChat(t *testing.T) {
	e, d := engine(t)
	ctx := context.Background()
	target := domain.ReplyTarget{MessageID: "forwarded", ChatID: "chat", Receiver: "+12025550100", From: "+12025550101", CreatedAt: time.Now()}
	e.Store.Update(func(tx *store.Tx) error { return tx.Put("replies", "forwarded", target) })
	// Original port now contains another card; the source SIM moved to module two.
	d.state = testState("one", "+12025550102", "new-card")
	other := &fakeDevice{state: testState("two", "+12025550100", "sim-one")}
	e.Modules["two"] = other
	in := incoming("reply", "正文  不是命令\n尾部  ")
	in.ParentID = "forwarded"
	e.Accept(ctx, in)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if d.sends != 0 || other.sends != 1 || other.targets[0] != target.From || other.texts[0] != in.Text {
		t.Fatal("reply misrouted")
	}
	bad := incoming("cross-chat", "text")
	bad.ParentID = "forwarded"
	bad.ChatID = "other-chat"
	e.Accept(ctx, bad)
	e.Tick(ctx)
	if other.sends != 1 {
		t.Fatal("cross-chat reply sent")
	}
	other.state.Online = false
	in.MessageID = "unplugged"
	e.Accept(ctx, in)
	e.Tick(ctx)
	if other.sends != 1 || d.sends != 0 {
		t.Fatal("reply fell back to wrong module")
	}
}

type parentLookup struct{ value domain.PostedMessage }

func (p parentLookup) LookupMessage(context.Context, string) (domain.PostedMessage, error) {
	return p.value, nil
}
func TestLegacyReplyRequiresOwnBotExactBodyAndStoredSMS(t *testing.T) {
	for _, variant := range []string{"valid", "other-bot", "altered", "other-chat"} {
		t.Run(variant, func(t *testing.T) {
			e, d := engine(t)
			id := domain.Key("legacy")
			m := domain.Message{ID: id, Identity: d.state.Identity, From: "+12025550101", Text: "原始短信", Kind: "sms", State: "complete", Parts: 1, Expected: 1, ReceivedAt: time.Now()}
			e.Store.Update(func(tx *store.Tx) error { return tx.Put("messages", id, m) })
			posted := domain.PostedMessage{MessageID: "legacy-provider", ChatID: "chat", Text: notify.Format(m), OwnBot: true}
			switch variant {
			case "other-bot":
				posted.OwnBot = false
			case "altered":
				posted.Text += "changed"
			case "other-chat":
				posted.ChatID = "other"
			}
			e.ParentLookup = parentLookup{posted}
			in := incoming("reply", "literal body  ")
			in.ParentID = "legacy-provider"
			e.Accept(context.Background(), in)
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := 0
			if variant == "valid" {
				want = 1
			}
			if d.sends != want {
				t.Fatal("unverified legacy reply sent", variant)
			}
		})
	}
}

func TestNationalSourceAndDestinationNormalizeTogether(t *testing.T) {
	e, d := engine(t)
	cn := "1" + "3" + strings.Repeat("0", 9)
	d.state = testState("one", "+86"+cn, "sim-one")
	e.Config.AllowedDestinations = []string{cn}
	e.Accept(context.Background(), incoming("national", "/sms "+cn+" "+cn+" test"))
	e.Tick(context.Background())
	if d.sends != 1 || d.targets[0] != "+86"+cn {
		t.Fatal("national number mismatch")
	}
}

func TestPreviouslyUnknownReceiverCanReplyAfterSameSIMIsConfigured(t *testing.T) {
	e, d := engine(t)
	target := domain.ReplyTarget{ChatID: "chat", Receiver: "one", SIM: "sim-one", From: "+12025550101"}
	e.Store.Update(func(tx *store.Tx) error { return tx.Put("replies", "parent", target) })
	in := incoming("configured", "reply")
	in.ParentID = "parent"
	e.Accept(context.Background(), in)
	e.Tick(context.Background())
	if d.sends != 1 {
		t.Fatal("same configured SIM not selected")
	}
	d.state = testState("one", "+12025550100", "replacement-sim")
	in.MessageID = "changed"
	e.Accept(context.Background(), in)
	e.Tick(context.Background())
	if d.sends != 1 {
		t.Fatal("old module/new SIM used for unknown original receiver")
	}
}

func TestNumericModuleIDCannotActAsAnUnknownPhoneNumber(t *testing.T) {
	e, d := engine(t)
	d.state = testState("50000", "50000", "sim-one")
	d.state.NumberSource = "module_id"
	e.Accept(context.Background(), incoming("numeric-id", "/sms 50000 +12025550101 test"))
	e.Tick(context.Background())
	if d.sends != 0 {
		t.Fatal("numeric module ID treated as a configured phone")
	}
}
