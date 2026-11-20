package phone

import (
	"strings"
	"testing"
)

func TestNormalizeCountryAndServiceCodes(t *testing.T) {
	cn := "1" + "3" + strings.Repeat("0", 9)
	for _, s := range []string{cn, "86" + cn, "+86" + cn, "  " + cn + "  "} {
		if Normalize(s) != "+86"+cn || !Equal(s, "+86"+cn) {
			t.Fatal("country normalization")
		}
	}
	for _, s := range []string{"50000", "+12025550100", "12025550100"} {
		if Normalize(s) != s {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"", "000000", "+", "12", "++50000", "50 000", "50000;exec", "☎50000"} {
		if Normalize(s) != "" {
			t.Fatal("invalid input accepted")
		}
	}
}
func FuzzNormalize(f *testing.F) {
	f.Add("+12025550100")
	f.Add("50000")
	f.Fuzz(func(t *testing.T, s string) {
		n := Normalize(s)
		if n != "" && Normalize(n) != n {
			t.Fatal("normalization is not idempotent")
		}
	})
}
