package protocol

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Payload and request bounds a well-behaved client keeps before sending
// (Apron server spec §6, "Payload and query bounds"). A client that checks
// first avoids a refused request - and for a single frame's size, a 1009
// close of the whole connection.
const (
	MaxFrameBytes       = 16384 // one text frame, UTF-8 bytes
	MaxMessageTextBytes = 4096  // one message's text
	MaxSnapshotBytes    = 8192  // a message snapshot as stored
	MaxRequestIDBytes   = 128   // one request id
	MaxNameCodePoints   = 80    // a display name, in code points
	MaxNameBytes        = 320   // a display name, in UTF-8 bytes
	MaxEmbeds           = 4     // embeds on one message
	MaxJSONDepth        = 8     // container levels; the root counts as 1
	MaxJSONNodes        = 2048  // values, counting root and array/object values

	HistoryDefaultLimit = 20 // a page's limit when none is sent
	HistoryMaxLimit     = 50 // the most records a history page may hold
)

// CheckFrame reports whether a marshaled frame stays within one frame's byte
// bound. The server closes a connection that sends a bigger one (1009).
func CheckFrame(data []byte) error {
	// A []byte holds UTF-8 already, so len is the UTF-8 byte count.
	if n := len(data); n > MaxFrameBytes {
		return &TooLargeError{What: "frame", Size: n, Limit: MaxFrameBytes}
	}
	return nil
}

// TooLargeError is a field or frame over the size bounds the server would
// refuse (too_large), or - for a whole frame - close the connection over
// (1009).
type TooLargeError struct {
	What  string // "frame", "message text", "request id", …
	Size  int    // UTF-8 bytes it is
	Limit int    // UTF-8 bytes it may be
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("%s is %d bytes; the limit is %d", e.What, e.Size, e.Limit)
}

// CheckText reports whether a message's text stays within its byte bound.
func CheckText(text string) error {
	if n := len(text); n > MaxMessageTextBytes {
		return &TooLargeError{What: "message text", Size: n, Limit: MaxMessageTextBytes}
	}
	return nil
}

// CheckID reports whether a request id stays within its byte bound.
func CheckID(id string) error {
	if n := len(id); n > MaxRequestIDBytes {
		return &TooLargeError{What: "request id", Size: n, Limit: MaxRequestIDBytes}
	}
	return nil
}

// CheckName reports whether a display name stays within the name bounds,
// which are both counted: code points and UTF-8 bytes.
func CheckName(name string) error {
	codePoints := utf8.RuneCountInString(name)
	bytes := len(name)
	if codePoints > MaxNameCodePoints || bytes > MaxNameBytes {
		return &TooLargeError{What: "name", Size: bytes, Limit: MaxNameBytes}
	}
	return nil
}

// limits is data.ext.limits on a retry_after error: the limit's category the
// request with it should hold back (extension ext:limits). "me" limits name
// no category.
type limits struct {
	Limits json.RawMessage `json:"limits"`
}

// extLimitsField is data.ext carrying a limits object.
type extLimitsField struct {
	Ext limits `json:"ext"`
}

// Limits returns the category (or categories) a retry_after error names in
// data.ext.limits - "read", "write" or "upload" - or nil when none. The
// daily SQL and frame budgets carry data.reason instead.
func (e *Error) Limits() []string {
	if e == nil || e.Data == nil {
		return nil
	}
	var d extLimitsField
	if err := json.Unmarshal(e.Data, &d); err != nil || d.Ext.Limits == nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal(d.Ext.Limits, &out); err != nil {
		// A single category comes as a string.
		var one string
		if json.Unmarshal(d.Ext.Limits, &one) == nil {
			return []string{one}
		}
		return nil
	}
	return out
}

// Reason returns data.reason, e.g. "daily_budget" or "frame_budget" on a
// retry_after raised by a server-wide budget.
func (e *Error) Reason() string {
	if e == nil || e.Data == nil {
		return ""
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return ""
	}
	return d.Reason
}
