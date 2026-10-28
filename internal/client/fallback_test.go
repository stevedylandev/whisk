package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestDialFallsBackToWSPath: a server that serves HTML at / and the socket at
// /ws (like aprond's bundled web client) is reachable from its root URL.
func TestDialFallsBackToWSPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"method":"server","params":{"apron":9,"auth":["guest"]}}`))
		// Read until the client closes, so the close handshake completes.
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	c, err := Dial(ctx, url)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer c.Close()
	select {
	case ev := <-c.Events():
		if ev.Method != "server" {
			t.Fatalf("first event = %q, want server", ev.Method)
		}
	case <-ctx.Done():
		t.Fatal("no server frame after fallback")
	}
}

func TestWSFallbackOnlyForRoot(t *testing.T) {
	if got, ok := wsFallbackURL("ws://localhost:8080/"); !ok || got != "ws://localhost:8080/ws" {
		t.Fatalf("root fallback = %q %v", got, ok)
	}
	if _, ok := wsFallbackURL("wss://chat.example/apron"); ok {
		t.Fatal("an explicit path must not be rewritten")
	}
}
