package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// TestHTTPRefusalSurfacesRetryAfter: a server that refuses the upgrade with
// 429 and a Retry-After yields an *HTTPError carrying exactly that, instead
// of an opaque dial failure.
func TestHTTPRefusalSurfacesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Connection admission limit reached"}`))
	}))
	defer srv.Close()

	c, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err == nil {
		c.Close()
		t.Fatal("a 429 refuse should not succeed")
	}
	var h *HTTPError
	if !errors.As(err, &h) {
		t.Fatalf("want an *HTTPError, got %T: %v", err, err)
	}
	if h.Status != http.StatusTooManyRequests {
		t.Fatalf("Status = %d", h.Status)
	}
	if h.Message != "Connection admission limit reached" {
		t.Fatalf("Message = %q", h.Message)
	}
	if h.RetryAfter != 90*time.Second {
		t.Fatalf("RetryAfter = %v", h.RetryAfter)
	}
	if !h.IsTemporary() {
		t.Fatal("a 429 is temporary")
	}
}

// TestDialRefusesPlainHTTPPage: a root URL answered with a plain page is a
// retry at /ws, whose own error surfaces with its status.
func TestDialRefusesPlainHTTPPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Origin not allowed"}`))
	}))
	defer srv.Close()

	_, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	var h *HTTPError
	if !errors.As(err, &h) || h.Status != http.StatusForbidden {
		t.Fatalf("want a 403 HTTPError, got %v", err)
	}
	if h.Message != "Origin not allowed" {
		t.Fatalf("Message = %q", h.Message)
	}
}

// TestOversizedRequestRefusedLocally: a request over the frame bound is
// refused before sending, with a too_large-style error, so the connection is
// not closed over it (1009).
func TestOversizedRequestRefusedLocally(t *testing.T) {
	c := &Client{pending: make(map[string]chan protocol.Response), events: make(chan Event, 1), done: make(chan struct{})}
	_, _, err := c.Request(context.Background(), "message", protocol.Message{
		Body: &protocol.Body{Text: strings.Repeat("x", 20000)},
	})
	if err == nil {
		t.Fatal("an oversized request must be refused")
	}
	tl, ok := err.(*protocol.TooLargeError)
	if !ok {
		t.Fatalf("want a TooLargeError, got %v", err)
	}
	if tl.Limit != protocol.MaxFrameBytes {
		t.Fatalf("Limit = %d", tl.Limit)
	}
}

// TestOversizedRequestIDRefusedLocally checks the id bound too.
func TestOversizedRequestIDRefusedLocally(t *testing.T) {
	c := &Client{pending: make(map[string]chan protocol.Response), events: make(chan Event, 1), done: make(chan struct{})}
	_, _, err := c.RequestRaw(context.Background(), "message", strings.Repeat("x", 200), nil)
	if err == nil {
		t.Fatal("an oversized id must be refused")
	}
	if _, ok := err.(*protocol.TooLargeError); !ok {
		t.Fatalf("want a TooLargeError, got %v", err)
	}
}

// TestAsCloseSurfacesCodeAndReason: a peer close carries its code and
// reason through Err, so the reconnect policy can tell a credential close
// from an ordinary one.
func TestAsCloseSurfacesCodeAndReason(t *testing.T) {
	c := &Client{}
	c.setErr(&disconnectedError{
		cause:     context.Canceled,
		CloseInfo: CloseInfo{Code: 1008, Reason: "Device token replaced"},
	})
	var ci CloseInfo
	if !AsClose(c.Err(), &ci) {
		t.Fatal("a disconnectedError carries close info")
	}
	if ci.Code != 1008 || ci.Reason != "Device token replaced" {
		t.Fatalf("ci = %+v", ci)
	}
	if c.CloseCode() != 1008 || c.CloseReason() != "Device token replaced" {
		t.Fatal("accessors should report the same")
	}
	// A plain network failure is not a clean close.
	c.setErr(context.Canceled)
	if c.CloseCode() != -1 || c.CloseReason() != "" {
		t.Fatal("no close frame, no code")
	}
	if AsClose(c.Err(), &ci) {
		t.Fatal("a plain error is not a close")
	}
}

// TestPingFrameIsTheExactText: the keepalive notification carries no params
// key, so it is byte for byte the server expects treatment for.
func TestPingFrameIsTheExactText(t *testing.T) {
	data, err := protocol.MarshalNotification("ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(`{"method":"ping"}`)) {
		t.Fatalf("ping frame = %s", data)
	}
}

// TestSilentForTracksReceive: markRecv/silentFor bound the watchdog's view.
func TestSilentForTracksReceive(t *testing.T) {
	c := &Client{}
	c.markRecv()
	if c.silentFor() > time.Second {
		t.Fatal("just received, silence should be tiny")
	}
	time.Sleep(20 * time.Millisecond)
	if c.silentFor() == 0 {
		t.Fatal("silence should advance")
	}
}

// TestHTTPErrorMessageFallsBackToStatus checks a body-less refusal.
func TestHTTPErrorMessageFallsBackToStatus(t *testing.T) {
	h := &HTTPError{Status: http.StatusServiceUnavailable, StatusText: "Service Unavailable"}
	if h.Error() != "connection refused: Service Unavailable" {
		t.Fatalf("Error() = %q", h.Error())
	}
	h2 := &HTTPError{Status: 429, StatusText: "Too Many Requests", RetryAfter: 60 * time.Second}
	if !h2.IsTemporary() || h2.RetryAfter != 60*time.Second {
		t.Fatal("temporary 429 with Retry-After")
	}
}
