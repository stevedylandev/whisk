package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarshalRequestRawParamsStaysObject: params passed as already-marshaled
// JSON (json.RawMessage or []byte) must be embedded as an object, not
// base64-encoded into a string. Re-marshaling a []byte is what Go's
// json.Marshal does, and the server answers such a frame with invalid_params
// "Params must be an object".
func TestMarshalRequestRawParamsStaysObject(t *testing.T) {
	for name, raw := range map[string]any{
		"RawMessage": json.RawMessage(`{"scheme":"guest"}`),
		"bytes":      []byte(`{"scheme":"guest"}`),
	} {
		data, err := MarshalRequest("auth", "i2", raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(data), `"params":{`) {
			t.Fatalf("%s: params is not an object: %s", name, data)
		}
		if strings.Contains(string(data), "c2NoZW1l") { // base64 of {"scheme…"
			t.Fatalf("%s: params got base64-encoded: %s", name, data)
		}
	}
	// Struct params are marshaled as an object as before.
	data, err := MarshalRequest("auth", "i1", struct {
		Scheme string `json:"scheme"`
	}{Scheme: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"params":{"scheme":"guest"}`) {
		t.Fatalf("struct params: %s", data)
	}
	// Nil and empty raw params are left out, like nil params.
	for _, raw := range []any{nil, json.RawMessage{}, []byte{}} {
		data, err := MarshalRequest("ping", "i3", raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "params") {
			t.Fatalf("empty params should be omitted: %s", data)
		}
	}
}
