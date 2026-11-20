package domain

import "testing"

func TestKeyBoundariesAndDeterminism(t *testing.T) {
	if Key("ab", "c") == Key("a", "bc") {
		t.Fatal("ambiguous boundaries")
	}
	if Key("a", "b") != Key("a", "b") || len(Key("x")) != 32 {
		t.Fatal("unstable key")
	}
}
