package bot

import (
	"errors"
	"strings"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/sms"
)

const Help = `短信机器人命令：
/modules                       模块和接收号码
/status [模块ID]               注册与短信状态
/sms 模块ID 目标号码 正文       发送一条短短信
/forward 模块ID query [类型]    查询呼叫转移
/forward 模块ID set 类型 号码   设置呼叫转移
/forward 模块ID cancel 类型     取消呼叫转移
类型：unconditional / busy / no_reply / unreachable；cancel 可用 all。
呼转取决于硬件/运营商适配器；不可用时会明确拒绝。短信提交成功不等于送达。`

type Command struct{ Kind, Module, To, Text, Action, Mode string }

func token(s string) (string, string) {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return s[:i], strings.TrimSpace(s[i:])
		}
	}
	return s, ""
}
func Parse(text string) (Command, error) {
	var c Command
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "@_user_") {
		_, text = token(text)
	}
	name, rest := token(text)
	name = strings.TrimPrefix(strings.ToLower(name), "/")
	c.Kind = name
	switch name {
	case "help", "start":
		if rest != "" {
			return c, errors.New("help不接受参数")
		}
		c.Kind = "help"
	case "modules":
		if rest != "" {
			return c, errors.New("modules不接受参数")
		}
	case "status":
		c.Module, rest = token(rest)
		if rest != "" {
			return c, errors.New("status参数过多")
		}
	case "sms":
		c.Module, rest = token(rest)
		c.To, c.Text = token(rest)
		if c.Module == "" || !config.ValidNumber(c.To) {
			return c, errors.New("格式：/sms 模块ID 目标号码 正文")
		}
		if err := sms.ValidateShort(c.Text); err != nil {
			return c, err
		}
	case "forward":
		c.Module, rest = token(rest)
		c.Action, rest = token(rest)
		c.Mode, rest = token(rest)
		if c.Mode == "" && c.Action == "query" {
			c.Mode = "unconditional"
		}
		if c.Action == "set" {
			c.To, rest = token(rest)
			if !config.ValidNumber(c.To) {
				return c, errors.New("呼转目标号码无效")
			}
		}
		validMode := c.Mode == "unconditional" || c.Mode == "busy" || c.Mode == "no_reply" || c.Mode == "unreachable" || (c.Mode == "all" && c.Action == "cancel")
		if c.Module == "" || rest != "" || !validMode || (c.Action != "set" && c.Action != "query" && c.Action != "cancel") {
			return c, errors.New("格式：/forward 模块ID query|set|cancel 类型 [号码]")
		}
	default:
		return c, errors.New("未知命令；发送 /help 查看帮助")
	}
	return c, nil
}
