package bot

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct{ in, kind, module string }{{"/help", "help", ""}, {"/status m1", "status", "m1"}, {"/modules", "modules", ""}, {"/sms m1 +12025550100 你好\n第二行", "sms", "m1"}, {"@_user_1 /sms m2 50000 101", "sms", "m2"}, {"/forward m1 query", "forward", "m1"}, {"/forward m1 set busy +12025550100", "forward", "m1"}, {"/forward m1 cancel all", "forward", "m1"}}
	for _, c := range tests {
		got, e := Parse(c.in)
		if e != nil || got.Kind != c.kind || got.Module != c.module {
			t.Fatal(c, got, e)
		}
	}
	c, _ := Parse("/sms m1 50000 a  b\n正文")
	if c.Text != "a  b\n正文" {
		t.Fatal(c.Text)
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
	f.Add("/sms m1 50000 101")
	f.Add("/forward m1 query")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 20000 {
			t.Skip()
		}
		_, _ = Parse(s)
	})
}
