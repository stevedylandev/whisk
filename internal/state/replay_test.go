package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// replayFixture is one official wire replay scenario (§2, "Replay scenarios").
type replayFixture struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Variants []replayVariant `json:"variants"`
	Expected json.RawMessage `json:"expected"`
}

type replayVariant struct {
	Name  string            `json:"name"`
	Steps []json.RawMessage `json:"steps"`
}

// step is one operation in a variant: exactly one of "receive" (a frame
// delivered to the client) or "expect" (an intermediate projection check).
type step struct {
	Receive json.RawMessage `json:"receive"`
	Expect  json.RawMessage `json:"expect"`
}

// receiveStep is one delivered frame: a notification (method + params) or a
// reply (id + result) to a history or room_list request.
type receiveStep struct {
	Method string          `json:"method"`
	ID     string          `json:"id"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// TestReplayFixtures drives the store through every official replay fixture
// and checks its projection against the expected state.
func TestReplayFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "replay", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no replay fixtures found: %v", err)
	}
	sort.Strings(files)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var fx replayFixture
		if err := json.Unmarshal(data, &fx); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		if fx.Kind != "replay" {
			continue
		}
		for _, v := range fx.Variants {
			t.Run(fx.Name+"/"+v.Name, func(t *testing.T) {
				runVariant(t, v, fx.Expected)
			})
		}
	}
}

// runVariant plays one variant twice: once with the literal frames, once with
// "jsonrpc": "2.0" added to each received frame, which the client must ignore.
func runVariant(t *testing.T, v replayVariant, expected json.RawMessage) {
	for _, inject := range []bool{false, true} {
		name := "literal"
		if inject {
			name = "jsonrpc"
		}
		t.Run(name, func(t *testing.T) {
			s := New()
			for _, stepRaw := range v.Steps {
				var st step
				if err := json.Unmarshal(stepRaw, &st); err != nil {
					t.Fatalf("bad step %s: %v", stepRaw, err)
				}
				if st.Expect != nil {
					checkProjection(t, s.Projection(), st.Expect, "expect")
					continue
				}
				if st.Receive == nil {
					continue
				}
				frame := st.Receive
				if inject {
					frame = injectJSONRPC(frame)
				}
				var recv receiveStep
				if err := json.Unmarshal(frame, &recv); err != nil {
					t.Fatalf("bad receive step %s: %v", frame, err)
				}
				applyReceive(s, &recv)
			}
			checkProjection(t, s.Projection(), expected, "expected")
		})
	}
}

// applyReceive delivers one received frame to the store.
func applyReceive(s *Store, recv *receiveStep) {
	if recv.Method != "" {
		switch recv.Method {
		case "message":
			var m protocol.Message
			if json.Unmarshal(recv.Params, &m) == nil {
				s.ApplyMessage(&m)
			}
		case "room_update":
			var p protocol.RoomUpdateParams
			if json.Unmarshal(recv.Params, &p) == nil {
				s.ApplyRoomUpdate(&p)
			}
		case "reactions":
			var p protocol.ReactionsParams
			if json.Unmarshal(recv.Params, &p) == nil {
				s.ApplyReactions(&p)
			}
		}
		return
	}
	if len(recv.Result) == 0 {
		return
	}
	// A reply to room_list carries joined/not_joined; everything else in the
	// replay fixtures is a room-less history result.
	var keys map[string]json.RawMessage
	if json.Unmarshal(recv.Result, &keys) != nil {
		return
	}
	if _, ok := keys["joined"]; ok {
		var res protocol.RoomListResult
		if json.Unmarshal(recv.Result, &res) == nil {
			s.ApplyRoomList(&res)
		}
		return
	}
	if _, ok := keys["not_joined"]; ok {
		var res protocol.RoomListResult
		if json.Unmarshal(recv.Result, &res) == nil {
			s.ApplyRoomList(&res)
		}
		return
	}
	var res protocol.HistoryResult
	if json.Unmarshal(recv.Result, &res) == nil {
		s.ApplyLogHistory(&res, false)
	}
}

// injectJSONRPC adds "jsonrpc": "2.0" to the top level of a received frame.
func injectJSONRPC(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	m["jsonrpc"] = json.RawMessage(`"2.0"`)
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// checkProjection compares the store's projection against an expected state,
// comparing JSON semantically (key order and number formatting are ignored,
// numbers compare by value).
func checkProjection(t *testing.T, got, want json.RawMessage, label string) {
	t.Helper()
	var g, w any
	gd := json.NewDecoder(bytes.NewReader(got))
	gd.UseNumber()
	if err := gd.Decode(&g); err != nil {
		t.Fatalf("%s: decode got %s: %v", label, got, err)
	}
	wd := json.NewDecoder(bytes.NewReader(want))
	wd.UseNumber()
	if err := wd.Decode(&w); err != nil {
		t.Fatalf("%s: decode want %s: %v", label, want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s mismatch:\n  got:  %s\n  want: %s", label, got, want)
	}
}
