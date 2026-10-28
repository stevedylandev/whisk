// Package config loads and saves the client's persisted settings and tokens.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Server is one saved server: its URL, the guest display name, and the latest
// auth token issued by it.
type Server struct {
	URL   string `json:"url"`
	Name  string `json:"name,omitempty"`
	Token string `json:"token,omitempty"`
}

// Config is the persisted client configuration.
type Config struct {
	Servers []Server `json:"servers,omitempty"`

	// Theme is the color theme: "light" for light terminals, else dark.
	Theme string `json:"theme,omitempty"`

	// Legacy single-server fields. Load migrates them into Servers, and Save
	// never writes them.
	Server string `json:"server,omitempty"`
	Name   string `json:"name,omitempty"`
	Token  string `json:"token,omitempty"`
}

// DefaultPath returns the config file path (~/.config/whisk/config.json).
func DefaultPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "whisk", "config.json")
}

// Load reads the config file, returning an empty Config if it does not exist.
// A legacy single-server config is migrated into Servers.
func Load(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.migrate()
	return cfg, nil
}

// migrate moves the legacy single-server fields into Servers and normalizes
// every server URL, merging entries that name the same server.
func (c *Config) migrate() {
	if c.Server != "" {
		c.Servers = append([]Server{{URL: c.Server, Name: c.Name, Token: c.Token}}, c.Servers...)
	}
	c.Server, c.Name, c.Token = "", "", ""
	servers := c.Servers
	c.Servers = nil
	for _, s := range servers {
		if u, err := NormalizeURL(s.URL); err == nil {
			s.URL = u
		}
		if c.Find(s.URL) < 0 {
			c.Servers = append(c.Servers, s)
		}
	}
}

// NormalizeURL turns a server address into the WebSocket URL to dial. It
// accepts ws, wss, http and https URLs (http maps to ws, https to wss), and a
// bare host such as "chat.example" defaults to wss (ws for a loopback host
// such as "localhost:8080"). Normalized URLs compare
// equal for the same server, whichever form was typed.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty server URL")
	}
	if !strings.Contains(raw, "://") {
		// A local server rarely has TLS: default loopback hosts to ws.
		scheme := "wss://"
		if h, err := url.Parse("//" + raw); err == nil && isLoopback(h.Hostname()) {
			scheme = "ws://"
		}
		raw = scheme + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %v", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "http":
		u.Scheme = "ws"
	case "wss", "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q (use ws, wss, http or https)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("server URL has no host")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// isLoopback reports whether host is localhost or a loopback address.
func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Find returns the index of the server with the given URL, or -1.
func (c *Config) Find(rawURL string) int {
	key := strings.TrimSpace(rawURL)
	if u, err := NormalizeURL(rawURL); err == nil {
		key = u
	}
	for i, s := range c.Servers {
		if s.URL == key {
			return i
		}
	}
	return -1
}

// Upsert replaces the saved server with the same URL, or appends it.
func (c *Config) Upsert(s Server) {
	if i := c.Find(s.URL); i >= 0 {
		c.Servers[i] = s
		return
	}
	c.Servers = append(c.Servers, s)
}

// Remove drops the saved server with the given URL, if present.
func (c *Config) Remove(url string) {
	if i := c.Find(url); i >= 0 {
		c.Servers = append(c.Servers[:i], c.Servers[i+1:]...)
	}
}

// Save writes the config file, creating parent directories as needed.
func Save(path string, cfg Config) error {
	cfg.migrate()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
