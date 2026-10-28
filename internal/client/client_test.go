package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/stevedylandev/whisk/internal/protocol"
)

func TestRequestReplyAndNotifications(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := context.Background()

		// Server greeting first.
		_ = conn.Write(ctx, websocket.MessageText, []byte(
			`{"method":"server","params":{"apron":9,"auth":["guest"]}}`))

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var f protocol.Frame
			if err := json.Unmarshal(data, &f); err != nil {
				return
			}
			switch f.Method {
			case "auth":
				_ = conn.Write(ctx, websocket.MessageText, []byte(
					`{"id":"`+f.ID+`","result":{"you":{"user_id":"u1","name":"Ada"}}}`))
			case "message":
				// Broadcast before the result.
				_ = conn.Write(ctx, websocket.MessageText, []byte(
					`{"method":"message","params":{"message_id":"123","log_id":"123","room_id":"general","from":{"user_id":"u1","name":"Ada"},"body":{"text":"hi"}}}`))
				_ = conn.Write(ctx, websocket.MessageText, []byte(
					`{"id":"`+f.ID+`","result":{"message_id":"123"}}`))
			default:
				_ = conn.Write(ctx, websocket.MessageText, []byte(
					`{"id":"`+f.ID+`","result":{}}`))
			}
		}
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx := context.Background()
	c, err := Dial(ctx, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Server frame arrives as a notification.
	select {
	case ev := <-c.Events():
		if ev.Method != "server" {
			t.Fatalf("expected server frame, got %q", ev.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server frame")
	}

	res, rpcErr, err := c.Request(ctx, "auth", protocol.AuthParams{Scheme: "guest"})
	if err != nil || rpcErr != nil {
		t.Fatalf("auth request: err=%v rpcErr=%v", err, rpcErr)
	}
	var ar protocol.AuthResult
	_ = json.Unmarshal(res, &ar)
	if ar.You == nil || ar.You.Name != "Ada" {
		t.Fatalf("auth result = %+v", ar)
	}

	res, _, err = c.Request(ctx, "message", protocol.Message{
		RoomID: "general",
		Body:   &protocol.Body{Text: "hi"},
	})
	if err != nil {
		t.Fatalf("message request: %v", err)
	}
	var mr protocol.MessageResult
	_ = json.Unmarshal(res, &mr)
	if mr.MessageID != "123" {
		t.Fatalf("message result = %+v", mr)
	}

	// The broadcast notification must have been delivered before the result.
	select {
	case ev := <-c.Events():
		if ev.Method != "message" {
			t.Fatalf("expected message event, got %q", ev.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message event")
	}
}

func TestUnsolicitedErrorDeliveredAsEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := context.Background()
		_ = conn.Write(ctx, websocket.MessageText, []byte(
			`{"method":"server","params":{"apron":9,"auth":["guest"]}}`))
		// An error that is not a reply to a request has no id (§1.1).
		_ = conn.Write(ctx, websocket.MessageText, []byte(
			`{"error":{"code":-32002,"message":"Server at capacity","data":{"retry_after":30}}}`))
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, err := Dial(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// First the server frame.
	select {
	case ev := <-c.Events():
		if ev.Method != "server" {
			t.Fatalf("expected server frame, got %q", ev.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server frame")
	}

	// Then the unsolicited error, delivered as an event with Error set.
	select {
	case ev := <-c.Events():
		if ev.Method != "" {
			t.Fatalf("error event method = %q, want empty", ev.Method)
		}
		if ev.Error == nil || ev.Error.Code != protocol.ErrRetryAfter {
			t.Fatalf("error event = %+v", ev.Error)
		}
		if got := ev.Error.RetryAfterSeconds(); got != 30 {
			t.Fatalf("retry_after = %v, want 30", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for unsolicited error")
	}
}

func TestNotifySendsNoID(t *testing.T) {
	got := make(chan protocol.Frame, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var f protocol.Frame
		_ = json.Unmarshal(data, &f)
		got <- f
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, err := Dial(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if err := c.Notify(context.Background(), "ping", nil); err != nil {
		t.Fatalf("notify: %v", err)
	}

	select {
	case f := <-got:
		if f.Method != "ping" {
			t.Fatalf("method = %q, want ping", f.Method)
		}
		if f.ID != "" {
			t.Fatalf("notification must not carry an id, got %q", f.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ping frame")
	}
}
