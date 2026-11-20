package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIValidation(t *testing.T) {
	if run(nil) == nil {
		t.Fatal("missing command")
	}
	if run([]string{"version"}) != nil {
		t.Fatal("version")
	}
	for _, args := range [][]string{{"unknown"}, {"run", "-bad"}, {"check", "-config", "missing"}, {"check", "extra"}} {
		if run(args) == nil {
			t.Fatal(args)
		}
	}
	p := filepath.Join(t.TempDir(), "config.json")
	c := map[string]any{"feishu": map[string]any{"app_id": "a", "app_secret": "s", "allowed_users": []string{"owner"}, "recipients": []map[string]string{{"type": "open_id", "id": "owner"}}}, "modules": []map[string]string{{"id": "one", "port": "fake"}}}
	b, _ := json.Marshal(c)
	os.WriteFile(p, b, 0600)
	if e := run([]string{"check", "-config", p}); e != nil {
		t.Fatal(e)
	}
	if run([]string{"send", "-config", p}) == nil {
		t.Fatal("send missing parameters")
	}
}
