package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stevedylandev/whisk/internal/client"
	"github.com/stevedylandev/whisk/internal/protocol"
)

// testClose builds a disconnect the way the read loop reports one: a clean
// peer close with a code and a reason.
func testClose(code int, reason string) error {
	return client.NewClose(context.Canceled, code, reason)
}

// --- Limits: reconnect policy ------------------------------------------------

// TestBackoffDelayBound: the backoff climbs 1 s to 60 s, with jitter that
// scales each rung between half and one and a half its base.
func TestBackoffDelayBound(t *testing.T) {
	for n := range 12 {
		d := backoffDelay(n)
		if d < time.Second {
			t.Fatalf("backoff(%d) = %v, under a second", n, d)
		}
		base := time.Second << uint(min(n, 6)) // rungs cap at 60 s (1<<6)
		capped := base
		if capped > backoffMax {
			capped = backoffMax
		}
		if over := base * 3 / 2; d > over && d > capped*3/2 {
			t.Fatalf("backoff(%d) = %v, over its jittered base %v", n, d, base)
		}
		if d < capped/2 {
			t.Fatalf("backoff(%d) = %v, under half its base", n, d)
		}
	}
}

// TestClassifyCloseCredentialCloseWaitsForUser: a close whose reason says the
// credentials were replaced must not reconnect automatically; the user is
// asked.
func TestClassifyCloseCredentialCloseWaitsForUser(t *testing.T) {
	for _, reason := range []string{
		"Device token replaced by another session",
		"Removed by an admin",
		"Signed out after a week without signing in or posting",
	} {
		err := testClose(1008, reason)
		delay, waitUser, note := classifyClose(err)
		if !waitUser {
			t.Errorf("close %q should wait for the user (delay %v)", reason, delay)
		}
		if note == "" {
			t.Errorf("close %q should carry a note", reason)
		}
	}
}

// TestClassifyClose1013WaitsMinutes: an address frame-rate or budget close is
// waited out in minutes, not the next second.
func TestClassifyClose1013WaitsMinutes(t *testing.T) {
	err := testClose(1013, "Frame rate limit reached")
	delay, waitUser, _ := classifyClose(err)
	if waitUser || delay < 2*time.Minute {
		t.Fatalf("1013: delay %v waitUser %v", delay, waitUser)
	}
	err2 := testClose(1013, "Demo capacity reached; try after daily reset")
	delay, _, _ = classifyClose(err2)
	if delay < 5*time.Minute {
		t.Fatalf("daily 1013 waits minutes: %v", delay)
	}
}

// TestClassifyCloseOrdinaryClimbs: stale (1001), delivery trouble (1011),
// and network failures get the ordinary climb.
func TestClassifyCloseOrdinaryClimbs(t *testing.T) {
	for name, e := range map[string]error{
		"stale":    testClose(1001, "Connection idle; reconnect to recover"),
		"delivery": testClose(1011, "Delivery failed; reconnect to recover"),
		"no close": nil,
		"backoff":  testClose(301, "moved permanently"),
	} {
		delay, waitUser, _ := classifyClose(e)
		if waitUser {
			t.Errorf("%s: waitUser", name)
		}
		if delay != 0 {
			t.Errorf("%s: delay %v, want ordinary 0", name, delay)
		}
	}
}

// TestBackoffResetsAfterStableConnection: attempts only start over once a
// connection stayed up a while.
func TestBackoffResetsAfterStableConnection(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()

	// A refusal alone, before any climb, waits exactly as asked.
	s.retryAfter = 17 * time.Second
	if d := m.backoff(s); d != 17*time.Second || s.retryAfter != 0 {
		t.Fatalf("backoff consuming refusal = %v retryAfter %v", d, s.retryAfter)
	}

	// A connection that never stayed up keeps the climb going.
	s.connectedAt = time.Now()
	m.backoff(s)
	if s.reconnectAttempts == 0 {
		t.Fatal("the climb should advance while unstable")
	}

	// Up long enough resets it to the first rung.
	s.connectedAt = time.Now().Add(-2 * time.Minute)
	b1 := m.backoff(s)
	if s.reconnectAttempts != 1 {
		t.Fatalf("attempts after reset = %d, want 1", s.reconnectAttempts)
	}
	if b1 > 2*time.Second {
		t.Fatalf("after a stable connection the climb starts small, got %v", b1)
	}
}

// --- Limits: posting bursts with the same id ---------------------------------

// echoConn starts a websocket server that answers every request with {}
// and reports the request ids it saw, so a test can check that a burst
// retry keeps the same id on the wire.
func echoConn(t *testing.T) (*client.Client, <-chan string) {
	t.Helper()
	ids := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var f protocol.Frame
			if json.Unmarshal(data, &f) != nil || f.ID == "" {
				continue
			}
			ids <- f.ID
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"id":"`+f.ID+`","result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("dial echo: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, ids
}

// TestRetryWriteUsesSameRequestID: a message a posting limit refused is
// re-sent with the same id after data.retry_after, so the server
// deduplicates it, and never beyond the retry cap.
func TestRetryWriteUsesSameRequestID(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	c, ids := echoConn(t)
	s.client = c
	defer func() { s.client = nil }()

	limited := func() requestDoneMsg {
		return requestDoneMsg{
			sid: s.id, tag: "message", c: c, method: "message",
			id: "id-1", rawParams: []byte(`{"body":{"text":"hi"}}`),
			rpcErr: &protocol.Error{
				Code:    protocol.ErrRetryAfter,
				Message: "Posting limit reached",
				Data:    json.RawMessage(`{"retry_after":1,"ext":{"limits":"write"}}`),
			},
		}
	}
	cmd := m.retryWrite(s, limited())
	if cmd == nil {
		t.Fatal("a write retry_after must be retried")
	}
	if done, ok := cmd().(requestDoneMsg); !ok || done.err != nil || done.rpcErr != nil {
		t.Fatalf("the retry should be accepted: %+v", done)
	}
	if got := <-ids; got != "id-1" {
		t.Fatalf("first attempt on the wire = %q, want id-1", got)
	}
	// The second refusal is retried once more, with the same id again.
	cmd2 := m.retryWrite(s, limited())
	if cmd2 == nil {
		t.Fatal("a second refusal has a retry left")
	}
	if done, ok := cmd2().(requestDoneMsg); !ok || done.err != nil {
		t.Fatalf("the second retry should go out: %+v", done)
	}
	if got := <-ids; got != "id-1" {
		t.Fatalf("retry on the wire = %q, want the same id-1", got)
	}

	// Past the retry cap, no more self-resends: the user resends.
	if cmd := m.retryWrite(s, limited()); cmd != nil {
		t.Fatal("retries stop at the cap")
	}

	// A write the server finally accepted is through with its count.
	m.handleRequestDone(s, requestDoneMsg{sid: s.id, tag: "message", id: "id-1", c: c, method: "message"})
	if c := m.retryWrite(s, limited()); c == nil {
		t.Fatal("an accepted write lets a burst retry like new again")
	}
}

// TestRetryWriteHonoursCategory: a retry_after of the read or upload
// category is not a write refusal and is not resent this way; a refusal with
// no category (a server-wide budget) is.
func TestRetryWriteHonoursCategory(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	c, ids := echoConn(t)
	s.client = c
	defer func() { s.client = nil }()

	read := requestDoneMsg{
		sid: s.id, tag: "message", c: c, method: "message", id: "id-2",
		rawParams: []byte(`{}`),
		rpcErr: &protocol.Error{
			Code:    protocol.ErrRetryAfter,
			Message: "History request limit reached",
			Data:    json.RawMessage(`{"retry_after":1,"ext":{"limits":"read"}}`),
		},
	}
	if cmd := m.retryWrite(s, read); cmd != nil {
		t.Fatal("a read limit does not re-send a write")
	}
	budget := read
	budget.rpcErr = &protocol.Error{
		Code:    protocol.ErrRetryAfter,
		Message: "Frame budget exhausted",
		Data:    json.RawMessage(`{"reason":"frame_budget","retry_after":1}`),
	}
	cmd := m.retryWrite(s, budget)
	if cmd == nil {
		t.Fatal("a server-wide budget refusal is retried")
	}
	if _, ok := cmd().(requestDoneMsg); !ok {
		t.Fatal("the retry should go out")
	}
	if got := <-ids; got != "id-2" {
		t.Fatalf("budget retry id = %q, want id-2", got)
	}
	// A gone connection makes no retry: the user resends instead.
	s.client = nil
	if cmd := m.retryWrite(s, budget); cmd != nil {
		t.Fatal("no retry after the taking connection is gone")
	}
}

// --- Limits: read-only guest ------------------------------------------------

// TestReadOnlyGuestRefused: a you with ext.permissions {"write": false} keeps
// whisk from sending posts, commands, or reactions; reading works.
func TestReadOnlyGuestRefused(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	s.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}, Capabilities: []string{"command", "rooms", "reactions"}})
	s.store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{
		UserID: "guest_1",
		Ext:    json.RawMessage(`{"permissions":{"write":false}}`),
	}})
	m.screen = screenChat

	if s.canWrite() {
		t.Fatal("a read-only guest reports cannot-write")
	}
	if m.requireWrite() {
		t.Fatal("requireWrite refuses on a read-only server")
	}
	if c := m.sendMessage("hi", ""); c != nil {
		t.Fatal("a post must not go out on a read-only server")
	}
	if c := m.sendCommand("/robot say hi", ""); c != nil {
		t.Fatal("a command must not go out on a read-only server")
	}
	// A guest still without the marker may write.
	s.store.ApplyAuth(&protocol.AuthResult{You: &protocol.User{UserID: "guest_2"}})
	if !s.canWrite() {
		t.Fatal("absent permission rules mean allowed")
	}
}

// --- Limits: sending size checks ---------------------------------------------

// TestOversizedTextRefusedLocally: composer text over 4,096 bytes is refused
// with a reason, and the composer keeps its content.
func TestOversizedTextRefusedLocally(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	s.store.ApplyServer(&protocol.Server{Apron: 9, Auth: []string{"guest"}})
	s.authed = true
	m.screen = screenChat
	defer m.chat.composer.Reset()

	oversized := make([]byte, protocol.MaxMessageTextBytes+80)
	for i := range oversized {
		oversized[i] = 'x'
	}
	m.chat.composer.SetValue(string(oversized))
	cmd := m.sendComposer()
	if cmd != nil {
		t.Fatal("oversized text must not send")
	}
	if m.status == "" {
		t.Fatal("the status line should say why")
	}
	if m.composerText() == "" {
		t.Fatal("the composer keeps its content; the user edits it down")
	}
	// A message the size does fit goes out as usual.
	small := make([]byte, protocol.MaxMessageTextBytes)
	for i := range small {
		small[i] = 'a'
	}
	m.chat.composer.SetValue(string(small))
	if cmd := m.sendComposer(); cmd == nil {
		t.Fatal("text at the bound sends")
	}
}

// TestReadCursorThrottled keeps the frames under the connection cap while
// reading a fast room: only one cursor goes out every readEvery.
func TestReadCursorThrottled(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	s.store.ApplyServer(&protocol.Server{Apron: 9, Capabilities: []string{"rooms", "history", "activity"}})
	s.authed = true
	s.store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general"}}})
	m.chat.roomID = "general"

	// Two unrelated messages arrive; the cursor follows the newest.
	s.store.ApplyMessage(&protocol.Message{MessageID: "10", LogID: "10", RoomID: "general", Body: &protocol.Body{Text: "a"}})
	s.store.ApplyMessage(&protocol.Message{MessageID: "20", LogID: "20", RoomID: "general", Body: &protocol.Body{Text: "b"}})

	if cmd := m.maybeSendRead(); cmd == nil {
		t.Fatal("a first cursor goes out")
	}
	if s.lastReadSent.IsZero() {
		t.Fatal("the sent time is recorded")
	}
	// A newer message within the window does not go out yet.
	s.store.ApplyMessage(&protocol.Message{MessageID: "30", LogID: "30", RoomID: "general", Body: &protocol.Body{Text: "c"}})
	if cmd := m.maybeSendRead(); cmd != nil {
		t.Fatal("a cursor within the throttle window waits")
	}
	if m.chat.lastReadID != "20" {
		t.Fatalf("lastReadID stays at the last sent position, got %q", m.chat.lastReadID)
	}
	// Once the window is over, the pending position goes out.
	s.lastReadSent = time.Now().Add(-readEvery)
	if cmd := m.maybeSendRead(); cmd == nil {
		t.Fatal("a cursor outside the window goes out")
	}
	if m.chat.lastReadID != "30" {
		t.Fatalf("lastReadID catches up, got %q", m.chat.lastReadID)
	}
}

// TestTypingThrottleInterval: typing waits typingEvery between updates.
func TestTypingThrottleInterval(t *testing.T) {
	m := newTestModel(t)
	s := m.cur()
	s.store.ApplyServer(&protocol.Server{Apron: 9, Capabilities: []string{"activity"}})
	s.authed = true
	s.store.ApplyRoomList(&protocol.RoomListResult{Joined: []protocol.Room{{RoomID: "general"}}})
	m.chat.roomID = "general"

	m.chat.composer.SetValue("hi")
	if cmds := m.maybeSendTyping(); len(cmds) == 0 {
		t.Fatal("a first typing update goes out")
	}
	m.chat.lastTypingSent = time.Now().Add(-3 * time.Second)
	if cmds := m.maybeSendTyping(); len(cmds) != 0 {
		t.Fatalf("typing waits %v between updates", typingEvery)
	}
	m.chat.lastTypingSent = time.Now().Add(-typingEvery)
	if cmds := m.maybeSendTyping(); len(cmds) == 0 {
		t.Fatal("typing outside the window goes out")
	}
}
