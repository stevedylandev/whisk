package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMigratesLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"server":"wss://a.example/","name":"Ada","token":"st_a"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %+v, want 1", cfg.Servers)
	}
	if got := cfg.Servers[0]; got.URL != "wss://a.example/" || got.Name != "Ada" || got.Token != "st_a" {
		t.Fatalf("migrated server = %+v", got)
	}
	if cfg.Server != "" || cfg.Token != "" {
		t.Fatalf("legacy fields not cleared: %+v", cfg)
	}

	// Saving writes only the servers list.
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"server"`) {
		t.Fatalf("legacy key written back: %s", data)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	var cfg Config
	cfg.Upsert(Server{URL: "wss://a/", Token: "1"})
	cfg.Upsert(Server{URL: "wss://b/"})
	cfg.Upsert(Server{URL: "wss://a/", Token: "2"})
	if len(cfg.Servers) != 2 || cfg.Servers[0].Token != "2" {
		t.Fatalf("servers = %+v", cfg.Servers)
	}
	cfg.Remove("wss://a/")
	if len(cfg.Servers) != 1 || cfg.Servers[0].URL != "wss://b/" {
		t.Fatalf("after remove = %+v", cfg.Servers)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"wss://chat.example/", "wss://chat.example/", false},
		{"ws://localhost:8080", "ws://localhost:8080/", false},
		{"https://chat.example/apron", "wss://chat.example/apron", false},
		{"http://127.0.0.1:9000/", "ws://127.0.0.1:9000/", false},
		{"  HTTPS://Chat.Example  ", "wss://chat.example/", false},
		{"chat.example", "wss://chat.example/", false},
		{"chat.example:8443/ws", "wss://chat.example:8443/ws", false},
		{"localhost:8080", "ws://localhost:8080/", false},
		{"127.0.0.1:8080", "ws://127.0.0.1:8080/", false},
		{"ftp://chat.example/", "", true},
		{"https://", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeURL(c.in)
		if c.err {
			if err == nil {
				t.Errorf("NormalizeURL(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestFindMatchesAnySchemeForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"servers":[{"url":"https://chat.example","token":"a"},{"url":"wss://chat.example/","token":"b"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].URL != "wss://chat.example/" {
		t.Fatalf("duplicate forms not merged: %+v", cfg.Servers)
	}
	if cfg.Find("https://chat.example/") != 0 || cfg.Find("chat.example") != 0 {
		t.Fatal("Find should match the same server in any form")
	}
}
