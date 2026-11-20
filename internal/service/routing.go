package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/notify"
	"github.com/kxn/mobilemod/internal/phone"
	"github.com/kxn/mobilemod/internal/store"
)

func (e *Engine) source(number string) (Device, modem.Snapshot, error) {
	n := phone.Normalize(number)
	if n == "" {
		return nil, modem.Snapshot{}, errors.New("发送号码无效")
	}
	var found Device
	var state modem.Snapshot
	for _, d := range e.Modules {
		s := d.Snapshot()
		if !s.Online || s.NumberSource == "module_id" || s.NumberSource == "unavailable" || !phone.Equal(s.Receiver, n) {
			continue
		}
		if found != nil {
			return nil, modem.Snapshot{}, errors.New("多个在线模块使用相同发送号码，请在管理页检查号码配置；未发送")
		}
		found = d
		state = s
	}
	if found == nil {
		return nil, modem.Snapshot{}, errors.New("未找到使用该发送号码的在线模块，请检查SIM和管理页号码配置；未发送")
	}
	if state.Identity.SIM == "" || state.Identity.SIM == "unknown" {
		return nil, state, errors.New("模块尚未识别SIM，未发送")
	}
	return found, state, nil
}

func (e *Engine) submitFrom(ctx context.Context, id, from, to, text string) string {
	d, s, err := e.source(from)
	if err != nil {
		return err.Error()
	}
	return e.submitExpected(ctx, d, id, s.ID, to, text, s)
}

func (e *Engine) handleSMSReply(ctx context.Context, job domain.CommandJob) error {
	var target domain.ReplyTarget
	err := e.Store.View(func(tx *store.Tx) error { return tx.Get("replies", job.Incoming.ParentID, &target) })
	if os.IsNotExist(err) && e.ParentLookup != nil {
		target, err = e.legacyReplyTarget(ctx, job.Incoming)
	}
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return e.reply(job, "引用的消息没有可验证的短信关联，未发送。请直接引用新版本转发的短信，或使用 /sms 发送号码 目标号码 正文。")
	}
	if target.ChatID != job.Incoming.ChatID {
		return e.reply(job, "引用消息不属于当前会话，未发送")
	}
	if phone.Normalize(target.Receiver) == "" || target.Receiver == target.Module {
		// A message received before manual number setup still has a SIM identity.
		// Resolve only that same SIM; never fall back to its old physical port.
		if target.SIM == "" || target.SIM == "unknown" {
			return e.reply(job, "原短信的接收号码和SIM未知，请使用 /sms 明确指定发送号码。")
		}
		for _, device := range e.Modules {
			state := device.Snapshot()
			if !state.Online || state.Identity.SIM != target.SIM {
				continue
			}
			selected, current, err := e.source(state.Receiver)
			if err != nil || current.Identity.SIM != target.SIM {
				continue
			}
			return e.reply(job, e.submitExpected(ctx, selected, job.Incoming.MessageID, current.ID, target.From, job.Incoming.Text, current))
		}
		return e.reply(job, "原短信的SIM不在线或尚未配置号码，未发送；请检查管理页。")
	}
	return e.reply(job, e.submitFrom(ctx, job.Incoming.MessageID, target.Receiver, target.From, job.Incoming.Text))
}

func (e *Engine) legacyReplyTarget(ctx context.Context, in domain.Incoming) (domain.ReplyTarget, error) {
	posted, err := e.ParentLookup.LookupMessage(ctx, in.ParentID)
	if err != nil || !posted.OwnBot || posted.ChatID != in.ChatID {
		return domain.ReplyTarget{}, os.ErrNotExist
	}
	header, _, _ := strings.Cut(posted.Text, "\n\n")
	id := ""
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "消息ID：") {
			id = strings.TrimPrefix(line, "消息ID：")
			break
		}
	}
	if len(id) != 32 {
		return domain.ReplyTarget{}, os.ErrNotExist
	}
	var source domain.Message
	if err := e.Store.View(func(tx *store.Tx) error { return tx.Get("messages", id, &source) }); err != nil {
		return domain.ReplyTarget{}, os.ErrNotExist
	}
	if source.Kind != "sms" {
		return domain.ReplyTarget{}, os.ErrNotExist
	}
	parts := notify.Split(notify.Format(source), 2500)
	match := false
	for i, p := range parts {
		if len(parts) > 1 {
			p = fmt.Sprintf("[转发 %d/%d]\n", i+1, len(parts)) + p
		}
		if p == posted.Text {
			match = true
			break
		}
	}
	if !match {
		return domain.ReplyTarget{}, os.ErrNotExist
	}
	target := domain.ReplyTarget{MessageID: in.ParentID, SourceID: source.ID, ChatID: in.ChatID, Receiver: source.Identity.Receiver, From: source.From, SIM: source.Identity.SIM, CreatedAt: time.Now(), Module: source.Identity.Module}
	err = e.Store.Update(func(tx *store.Tx) error { return tx.Put("replies", in.ParentID, target) })
	return target, err
}
