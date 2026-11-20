package service

import (
	"context"
	"errors"
	"os"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/modem"
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
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return e.reply(job, "找不到这条引用消息对应的短信，请使用 /sms 发送号码 目标号码 正文。")
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
