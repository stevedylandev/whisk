package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckFrameAndTextAndID(t *testing.T) {
	if err := CheckFrame(make([]byte, MaxFrameBytes)); err != nil {
		t.Fatalf("a frame of exactly the limit should pass: %v", err)
	}
	err := CheckFrame(make([]byte, MaxFrameBytes+1))
	if err == nil || !strings.Contains(err.Error(), "frame") {
		t.Fatalf("one byte over should fail, got %v", err)
	}
	if err := CheckText(strings.Repeat("a", MaxMessageTextBytes)); err != nil {
		t.Fatalf("text of exactly the limit should pass: %v", err)
	}
	if err := CheckText(strings.Repeat("a", MaxMessageTextBytes+1)); err == nil {
		t.Fatal("text one byte over should fail")
	}
	if err := CheckID(strings.Repeat("i", MaxRequestIDBytes)); err != nil {
		t.Fatalf("id of exactly the limit should pass: %v", err)
	}
	if err := CheckID(strings.Repeat("i", MaxRequestIDBytes+1)); err == nil {
		t.Fatal("id one byte over should fail")
	}
	if err := CheckName(strings.Repeat("é", MaxNameCodePoints)); err != nil {
		// 80 code points at 2 bytes each = 160 bytes, within 320.
		t.Fatalf("80 two-byte code points should fit: %v", err)
	}
	if err := CheckName(strings.Repeat("é", MaxNameCodePoints+1)); err == nil {
		t.Fatal("81 code points should fail")
	}
	// 40 emoji are 160 bytes only; 81 are 324 bytes and 81 code points:
	// both bounds go over.
	if err := CheckName(strings.Repeat("\U0001F600", 81)); err == nil {
		t.Fatal("a name over 320 bytes should fail")
	}
}

func TestErrorLimits(t *testing.T) {
	e := func(data string) *Error {
		return &Error{Code: ErrRetryAfter, Message: "x", Data: json.RawMessage(data)}
	}
	tests := []struct {
		data string
		want []string
	}{
		{`{"retry_after":60,"ext":{"limits":"write"}}`, []string{"write"}},
		{`{"retry_after":1,"ext":{"limits":["read"]}}`, []string{"read"}},
		{`{"retry_after":5}`, nil},
		{`{"reason":"daily_budget","retry_after":3}`, nil},
		{``, nil},
		{`{bad`, nil},
	}
	for _, c := range tests {
		got := e(c.data).Limits()
		if len(got) != len(c.want) {
			t.Errorf("Limits(%s) = %v, want %v", c.data, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Limits(%s) = %v, want %v", c.data, got, c.want)
			}
		}
	}
	if got := e(`{"reason":"frame_budget","retry_after":2}`).Reason(); got != "frame_budget" {
		t.Errorf("Reason = %q, want frame_budget", got)
	}
}

func TestCanWriteGuestPermissions(t *testing.T) {
	guest := &User{UserID: "guest_1", Ext: json.RawMessage(`{"permissions":{"write":false}}`)}
	if CanWrite(guest) {
		t.Fatal("a guest with write:false may not write")
	}
	writeable := &User{UserID: "u1", Ext: json.RawMessage(`{"permissions":{"write":true}}`)}
	if !CanWrite(writeable) {
		t.Fatal("write:true is allowed")
	}
	plain := &User{UserID: "u2"}
	if !CanWrite(plain) {
		t.Fatal("a user without ext data may write")
	}
	if !CanWrite(nil) {
		t.Fatal("no user defaults to allowed")
	}
	emojiExt := &User{UserID: "u3", Ext: json.RawMessage(`{"other":1}`)}
	if !CanWrite(emojiExt) {
		t.Fatal("unrelated ext data is not a permission denial")
	}
}

func TestServerMedia(t *testing.T) {
	srv := &Server{Ext: json.RawMessage(`{"media":{"upload":{"image/png":{"max_bytes":5242880}},"avatar":{"image/png":{"max_bytes":262144}}}}`)}
	m := ServerMedia(srv)
	if m == nil {
		t.Fatal("media should parse")
	}
	if m.Upload["image/png"].MaxBytes != 5242880 {
		t.Fatalf("upload cap = %d", m.Upload["image/png"].MaxBytes)
	}
	if m.Avatar["image/png"].MaxBytes != 262144 {
		t.Fatalf("avatar cap = %d", m.Avatar["image/png"].MaxBytes)
	}
	if ServerMedia(&Server{}) != nil {
		t.Fatal("a server without ext has no media")
	}
}
