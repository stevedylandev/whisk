package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/stevedylandev/whisk/internal/protocol"
)

func TestMarkdownRendersAndCaches(t *testing.T) {
	start := time.Now()
	r := newRenderer(80)

	out := r.Markdown("**bold** and *italic* and `code`")
	if !strings.Contains(out, "bold") {
		t.Fatalf("markdown output missing content: %q", out)
	}

	// The cache must return the identical result without re-rendering.
	out2 := r.Markdown("**bold** and *italic* and `code`")
	if out != out2 {
		t.Fatal("markdown cache returned a different result")
	}

	// A different input must produce a different (but valid) result.
	if r.Markdown("# Heading") == "" {
		t.Fatal("empty output for heading")
	}

	// Rendering must not block on a terminal background-color query. It should
	// finish in well under the ~5s OSC timeout; use a generous bound.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("markdown render took %s, possible terminal query block", elapsed)
	}
}

func TestSystemSenderDetection(t *testing.T) {
	cases := []struct {
		user *protocol.User
		want bool
	}{
		{&protocol.User{UserID: "~server"}, true},
		{&protocol.User{UserID: "~room"}, true},
		{&protocol.User{UserID: "~private"}, true},
		{&protocol.User{UserID: "u1"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isSystemSender(c.user); got != c.want {
			t.Errorf("isSystemSender(%v) = %v, want %v", c.user, got, c.want)
		}
	}
}
