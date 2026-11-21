// Package sms durably reassembles independently decoded SMS fragments. It never
// treats a missing/conflicting fragment as a complete message.
package sms

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/notify"
	"github.com/kxn/mobilemod/internal/store"
)

type Group struct {
	ID       string          `json:"id"`
	Identity domain.Identity `json:"identity"`
	From     string          `json:"from"`
	Ref      int             `json:"ref"`
	Total    int             `json:"total"`
	Parts    map[int]string  `json:"parts"`
	Started  time.Time       `json:"started"`
	Deadline time.Time       `json:"deadline"`
	Closed   bool            `json:"closed"`
	Conflict bool            `json:"conflict"`
}
type Raw struct {
	Identity domain.Identity `json:"identity"`
	Envelope domain.Envelope `json:"envelope"`
	At       time.Time       `json:"at"`
}
type Assembler struct {
	Store      *store.Store
	Recipients []domain.Recipient
	// RecipientsProvider, when set, supplies the current forwarding targets.
	// It lets the runtime follow the latest Feishu p2p sender without racing
	// the slice used by tests and static configurations.
	RecipientsProvider func() []domain.Recipient
	Timeout            time.Duration
	MaxParts           int
}

func (a *Assembler) recipients() []domain.Recipient {
	if a.RecipientsProvider != nil {
		if r := a.RecipientsProvider(); len(r) > 0 {
			return r
		}
	}
	return a.Recipients
}

func (a *Assembler) Ingest(identity domain.Identity, e domain.Envelope, now time.Time) error {
	if e.MessageID <= 0 || e.BootID == "" {
		return fmt.Errorf("missing durable event identity")
	}
	if !utf8.ValidString(e.Text) || !utf8.ValidString(e.From) {
		return fmt.Errorf("invalid UTF-8")
	}
	rawID := domain.Key(identity.Module, e.BootID, fmt.Sprint(e.MessageID))
	return a.Store.Update(func(tx *store.Tx) error {
		if tx.Has("raw", rawID) {
			return nil
		}
		if err := tx.Put("raw", rawID, Raw{identity, e, now}); err != nil {
			return err
		}
		base := domain.Message{ID: rawID, Identity: identity, From: e.From, Text: e.Text, ReceivedAt: now, Kind: "sms", State: "complete", Parts: 1, Expected: 1}
		if e.Event == "call_received" {
			base.Kind = "call"
			base.Text = ""
			if e.From == "" {
				base.From = "未知（固件未提供来电号码）"
			}
			if e.OccurredAt >= 1600000000 && e.OccurredAt <= now.Add(5*time.Minute).Unix() {
				base.ReceivedAt = time.Unix(e.OccurredAt, 0)
			} else {
				base.TimeNote = "设备未提供有效时间，以下为电脑收到通知的时间"
			}
			return notify.EnqueueMessage(tx, base, a.recipients())
		}
		if e.Event == "mms_received" {
			base.Kind = "mms"
			base.State = "unsupported"
			base.Text = "当前模块没有彩信载荷下载接口。通知内容：\n" + e.Text
			if e.ContentLocation != "" {
				base.Text += "\n载荷地址（未访问）：" + e.ContentLocation
			}
			return notify.EnqueueMessage(tx, base, a.recipients())
		}
		m := e.Metadata
		if m.Total >= 0 && m.Total <= 1 && m.Sequence >= 0 && m.Sequence <= 1 && m.Ref >= 0 {
			return notify.EnqueueMessage(tx, base, a.recipients())
		}
		if m.Total < 2 || m.Total > a.MaxParts || m.Sequence < 1 || m.Sequence > m.Total || m.Ref < 0 || m.Ref > 65535 {
			base.State = "invalid"
			base.Expected = max(1, m.Total)
			return notify.EnqueueMessage(tx, base, a.recipients())
		}
		key := domain.Key(identity.Module, identity.SIM, e.From, fmt.Sprint(m.Ref), fmt.Sprint(m.Total))
		var g Group
		err := tx.Get("groups", key, &g)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		// Enforce the deadline here too: a delayed expiration tick must not
		// allow a late fragment to turn an expired group into a complete SMS.
		if err == nil && !g.Closed && !now.Before(g.Deadline) {
			g.Closed = true
			g.Deadline = now.Add(a.Timeout)
			if err := notify.EnqueueMessage(tx, finish(g), a.recipients()); err != nil {
				return err
			}
			if err := tx.Put("groups", key, g); err != nil {
				return err
			}
		}
		if err == nil && g.Closed && now.Before(g.Deadline) {
			if g.Parts[m.Sequence] == e.Text {
				return nil
			}
			// A reference was reused while a completed group is still retained.
			// Preserve the new raw fragment and warn instead of mixing messages.
			base.State = "ambiguous"
			base.Expected = m.Total
			base.Text = "短信参考编号发生复用或冲突，以下是独立保留的分片：\n" + e.Text
			return notify.EnqueueMessage(tx, base, a.recipients())
		}
		if err != nil || g.Closed {
			g = Group{ID: rawID, Identity: identity, From: e.From, Ref: m.Ref, Total: m.Total, Parts: map[int]string{}, Started: now, Deadline: now.Add(a.Timeout)}
		}
		if old, ok := g.Parts[m.Sequence]; ok {
			if old == e.Text {
				return nil
			}
			g.Conflict = true
			base.State = "ambiguous"
			base.Expected = m.Total
			base.Text = fmt.Sprintf("第%d片收到互不相同的内容，未替换原片。冲突片：\n%s", m.Sequence, e.Text)
			if err := notify.EnqueueMessage(tx, base, a.recipients()); err != nil {
				return err
			}
		} else {
			g.Parts[m.Sequence] = e.Text
		}
		if len(g.Parts) == g.Total {
			g.Closed = true
			if err := notify.EnqueueMessage(tx, finish(g), a.recipients()); err != nil {
				return err
			}
		}
		return tx.Put("groups", key, g)
	})
}
func finish(g Group) domain.Message {
	m := domain.Message{ID: g.ID, Identity: g.Identity, From: g.From, ReceivedAt: g.Started, Kind: "sms", State: "complete", Parts: len(g.Parts), Expected: g.Total}
	var b strings.Builder
	for i := 1; i <= g.Total; i++ {
		p, ok := g.Parts[i]
		if !ok {
			m.Missing = append(m.Missing, i)
			fmt.Fprintf(&b, "\n[缺少第%d片]\n", i)
		} else {
			b.WriteString(p)
		}
	}
	m.Text = b.String()
	if len(m.Missing) > 0 {
		m.State = "partial"
	}
	if g.Conflict {
		m.State = "ambiguous"
	}
	return m
}
func (a *Assembler) Expire(now time.Time) error {
	return a.Store.Update(func(tx *store.Tx) error {
		var expired = map[string]Group{}
		if err := tx.Scan("groups", func(k string, b []byte) error {
			var g Group
			if err := json.Unmarshal(b, &g); err != nil {
				return err
			}
			if !g.Closed && !now.Before(g.Deadline) {
				expired[k] = g
			}
			return nil
		}); err != nil {
			return err
		}
		for k, g := range expired {
			g.Closed = true
			g.Deadline = now.Add(a.Timeout)
			if err := notify.EnqueueMessage(tx, finish(g), a.recipients()); err != nil {
				return err
			}
			if err := tx.Put("groups", k, g); err != nil {
				return err
			}
		}
		return nil
	})
}
