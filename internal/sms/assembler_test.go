package sms

import (
	"encoding/json"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func setup(t *testing.T) (*Assembler, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.db")
	s, e := store.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return &Assembler{Store: s, Timeout: time.Minute, MaxParts: 32, Recipients: []domain.Recipient{{Type: "open_id", ID: "owner"}}}, p
}
func part(id int64, seq, total, ref int, text string) domain.Envelope {
	return domain.Envelope{Event: "sms_received", BootID: "boot", MessageID: id, From: "+12025550100", Text: text, Metadata: domain.Metadata{Ref: ref, Sequence: seq, Total: total}}
}
func messages(t *testing.T, a *Assembler) []domain.Message {
	t.Helper()
	var out []domain.Message
	e := a.Store.View(func(tx *store.Tx) error {
		return tx.Scan("messages", func(_ string, b []byte) error {
			var m domain.Message
			if e := json.Unmarshal(b, &m); e != nil {
				return e
			}
			out = append(out, m)
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

var identity = domain.Identity{Module: "m1", Receiver: "+12025550101", SIM: "sim-a"}

func TestOutOfOrderDuplicateAndRestart(t *testing.T) {
	a, p := setup(t)
	now := time.Now()
	if e := a.Ingest(identity, part(1, 3, 3, 7, "尾"), now); e != nil {
		t.Fatal(e)
	}
	if e := a.Ingest(identity, part(2, 1, 3, 7, "头"), now); e != nil {
		t.Fatal(e)
	}
	if e := a.Ingest(identity, part(3, 1, 3, 7, "头"), now); e != nil {
		t.Fatal(e)
	}
	if len(messages(t, a)) != 0 {
		t.Fatal("premature assembly")
	}
	a.Store.Close()
	db, e := store.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	a.Store = db
	t.Cleanup(func() { db.Close() })
	if e = a.Ingest(identity, part(4, 2, 3, 7, "中"), now); e != nil {
		t.Fatal(e)
	}
	ms := messages(t, a)
	if len(ms) != 1 || ms[0].Text != "头中尾" || ms[0].State != "complete" {
		t.Fatal(ms)
	}
	a.Ingest(identity, part(4, 2, 3, 7, "中"), now)
	a.Ingest(identity, part(5, 2, 3, 7, "中"), now)
	if len(messages(t, a)) != 1 {
		t.Fatal("duplicate delivery")
	}
	a.Store.View(func(tx *store.Tx) error {
		if tx.Count("outbox") != 1 {
			t.Fatal("non-atomic outbox")
		}
		return nil
	})
}
func TestIsolationByModuleSIMSenderAndTotal(t *testing.T) {
	a, _ := setup(t)
	now := time.Now()
	for i := 0; i < 4; i++ {
		id := identity
		e := part(int64(i+1), 1, 2, 9, "A")
		if i == 1 {
			id.Module = "m2"
		}
		if i == 2 {
			id.SIM = "sim-b"
		}
		if i == 3 {
			e.From = "50000"
		}
		if err := a.Ingest(id, e, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(messages(t, a)) != 0 {
		t.Fatal("mixed identities")
	}
	a.Ingest(identity, part(9, 2, 2, 9, "B"), now)
	ms := messages(t, a)
	if len(ms) != 1 || ms[0].Text != "AB" {
		t.Fatal(ms)
	}
}
func TestMissingAndConflictingFragments(t *testing.T) {
	a, _ := setup(t)
	now := time.Now()
	a.Ingest(identity, part(1, 2, 3, 1, "中"), now)
	if e := a.Expire(now.Add(2 * time.Minute)); e != nil {
		t.Fatal(e)
	}
	ms := messages(t, a)
	if len(ms) != 1 || ms[0].State != "partial" || len(ms[0].Missing) != 2 {
		t.Fatal(ms)
	}
	a.Ingest(identity, part(2, 1, 2, 2, "原"), now)
	a.Ingest(identity, part(3, 1, 2, 2, "冲突"), now)
	a.Ingest(identity, part(4, 2, 2, 2, "尾"), now)
	ms = messages(t, a)
	for _, m := range ms {
		if m.Expected == 2 && m.State == "complete" {
			t.Fatal("conflict passed as complete")
		}
	}
}
func TestMalformedMMSAndReferenceReuse(t *testing.T) {
	a, _ := setup(t)
	now := time.Now()
	if e := a.Ingest(identity, domain.Envelope{Event: "sms_received"}, now); e == nil {
		t.Fatal("accepted missing identity")
	}
	a.Ingest(identity, part(1, 0, 2, 1, "invalid"), now)
	a.Ingest(identity, part(2, 1, 1, 2, "plain"), now)
	mms := part(3, 0, 0, 0, "通知")
	mms.Event = "mms_received"
	mms.ContentLocation = "http://127.0.0.1/private"
	a.Ingest(identity, mms, now)
	a.Ingest(identity, part(4, 1, 2, 3, "a"), now)
	a.Ingest(identity, part(5, 2, 2, 3, "b"), now)
	a.Ingest(identity, part(6, 1, 2, 3, "different"), now)
	states := map[string]bool{}
	for _, m := range messages(t, a) {
		states[m.State] = true
	}
	for _, s := range []string{"invalid", "complete", "unsupported", "ambiguous"} {
		if !states[s] {
			t.Fatal("missing state", s)
		}
	}
}

func TestLateFragmentCannotCompleteExpiredGroup(t *testing.T) {
	a, _ := setup(t)
	now := time.Now()
	if err := a.Ingest(identity, part(1, 1, 2, 4, "first"), now); err != nil {
		t.Fatal(err)
	}
	// No expiration worker has run yet.
	if err := a.Ingest(identity, part(2, 2, 2, 4, "late"), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, a)
	if len(ms) != 2 {
		t.Fatal(ms)
	}
	states := map[string]bool{}
	for _, m := range ms {
		states[m.State] = true
	}
	if !states["partial"] || !states["ambiguous"] || states["complete"] {
		t.Fatal(ms)
	}
}

func TestNegativeMetadataIsPreservedAsInvalid(t *testing.T) {
	a, _ := setup(t)
	if err := a.Ingest(identity, part(1, -1, -2, -3, "preserve me"), time.Now()); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, a)
	if len(ms) != 1 || ms[0].State != "invalid" || ms[0].Text != "preserve me" {
		t.Fatal(ms)
	}
}

func TestCallNoticePreservesEventTimeAndNoReplyCorrelation(t *testing.T) {
	a, _ := setup(t)
	now := time.Now()
	event := part(1, 0, 0, 0, "")
	event.Event = "call_received"
	event.From = ""
	event.OccurredAt = now.Add(-time.Hour).Unix()
	if err := a.Ingest(identity, event, now); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, a)
	if len(ms) != 1 || ms[0].Kind != "call" || ms[0].ReceivedAt.Unix() != event.OccurredAt || ms[0].From == "" {
		t.Fatal(ms)
	}
	event.MessageID = 2
	event.OccurredAt = 0
	if err := a.Ingest(identity, event, now); err != nil {
		t.Fatal(err)
	}
	for _, m := range messages(t, a) {
		if m.ID == domain.Key(identity.Module, event.BootID, "2") && m.TimeNote == "" {
			t.Fatal("missing timestamp caveat")
		}
	}
}
