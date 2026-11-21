package bot

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct{ in, kind, module string }{{"/help", "help", ""}, {"/status m1", "status", "m1"}, {"/modules", "modules", ""}, {"/sms +12025550100 +12025550100 你好\n第二行", "sms", ""}, {"@_user_1 /sms +12025550101 50000 101", "sms", ""}, {"/forward +12025550100 query", "forward", ""}, {"/forward +12025550100 set busy +12025550100", "forward", ""}, {"/forward +12025550100 cancel all", "forward", ""}}
	for _, c := range tests {
		got, e := Parse(c.in)
		if e != nil || got.Kind != c.kind || got.Module != c.module {
			t.Fatal(c, got, e)
		}
	}
	c, _ := Parse("/sms +12025550100 50000 a  b\n正文")
	if c.Text != "a  b\n正文" {
		t.Fatal(c.Text)
	}
}
func TestHelpHasNoDisclaimer(t *testing.T) {
	for _, s := range []string{"不代表", "不等于", "取决于", "明确拒绝", "送达"} {
		if strings.Contains(Help, s) {
			t.Fatalf("help contains %q: %s", s, Help)
		}
	}
}

func TestRejectMalformedCommands(t *testing.T) {
	for _, s := range []string{"", "/sms", "/sms a +86;echo x", "/sms a 50000 ", "/status a b", "/help extra", "/forward a cancel random", "/forward a set all 50000", "/forward a set busy 50000 extra", "/sms a 50000 " + strings.Repeat("中", 71), "/lua os.execute()"} {
		if _, e := Parse(s); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
func FuzzParse(f *testing.F) {
	f.Add("/sms +12025550100 50000 101")
	f.Add("/forward +12025550100 query")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 20000 {
			t.Skip()
		}
		_, _ = Parse(s)
	})
}

func TestSMSPayloadWhitespaceAndNationalNumbers(t *testing.T) {
	cn := "1" + "3" + strings.Repeat("0", 9)
	for _, body := range []string{"正文  内部空格  ", " 第一行\n第二行\t  ", "/help 不是命令\r\n"} {
		c, err := Parse("/sms " + cn + " " + cn + " " + body)
		if err != nil || c.From != "+86"+cn || c.To != "+86"+cn || c.Text != body {
			t.Fatalf("payload changed: %q %v", c.Text, err)
		}
	}
	if _, err := Parse("/sms " + cn + " " + cn + "   \n\t "); err == nil {
		t.Fatal("blank-only SMS accepted")
	}
}
