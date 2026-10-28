package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHistoryParamsRoomless: a room-less history request (§4.3) omits the
// room_id key; an empty id would ask for room "" instead of the whole log.
func TestHistoryParamsRoomless(t *testing.T) {
	b, err := json.Marshal(HistoryParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "room_id") {
		t.Fatalf("room-less history carries room_id: %s", b)
	}

	b, _ = json.Marshal(HistoryParams{RoomID: "general", Before: "10"})
	if !strings.Contains(string(b), `"room_id":"general"`) {
		t.Fatalf("room history lost room_id: %s", b)
	}
}
