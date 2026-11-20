package forwarding

import (
	"errors"
	"github.com/kxn/mobilemod/internal/bot"
	"github.com/kxn/mobilemod/internal/config"
	"testing"
)

func TestUnsupportedAndExplicitTemplates(t *testing.T) {
	_, _, e := Build(config.Forwarding{}, bot.Command{Action: "query"})
	if !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	cfg := config.Forwarding{Backend: "sms", ServiceNumber: "50000", Query: "QUERY", Set: map[string]string{"unconditional": "TEST#{number}"}, Cancel: map[string]string{"all": "CANCEL"}}
	to, text, e := Build(cfg, bot.Command{Action: "set", Mode: "unconditional", To: "+12025550100"})
	if e != nil || to != "50000" || text != "TEST#+12025550100" {
		t.Fatal(to, text, e)
	}
	for _, c := range []bot.Command{{Action: "set", Mode: "unconditional", To: "+86;evil"}, {Action: "query", Mode: "busy"}, {Action: "cancel", Mode: "busy"}} {
		if c.Action == "query" {
			cfg.Query = ""
		}
		if _, _, e := Build(cfg, c); e == nil {
			t.Fatal("accepted unsupported", c)
		}
	}
}
