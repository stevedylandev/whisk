// Package protocol defines the Apron Chat Protocol wire types and error codes.
//
// Frames use the shape of JSON-RPC 2.0 requests, responses and notifications,
// without the "jsonrpc" key, and every id is a string. See https://apron.chat.
package protocol

import "encoding/json"

// Frame is a single JSON object on the wire: a request, a reply, or a
// notification. The fields that are absent in a given frame are left zero.
type Frame struct {
	Method string          `json:"method,omitempty"`
	ID     string          `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC style error carried by an error reply.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Response is a reply matched to a request by its id.
type Response struct {
	ID     string
	Result json.RawMessage
	Error  *Error
}

// Error codes. Clients identify an error by its code only.
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrUnsupported    = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
	ErrDenied         = -32001
	ErrRetryAfter     = -32002
	ErrTooLarge       = -32003
)

// RetryAfterSeconds returns the data.retry_after delay in seconds for a
// retry_after error, or 0 when absent.
func (e *Error) RetryAfterSeconds() float64 {
	if e == nil || e.Data == nil {
		return 0
	}
	var d struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return 0
	}
	return d.RetryAfter
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// MarshalRequest builds a request frame: {"method","id","params"}.
func MarshalRequest(method, id string, params any) ([]byte, error) {
	return marshalFrame(Frame{Method: method, ID: id}, params)
}

// MarshalNotification builds a notification frame: {"method","params"}.
func MarshalNotification(method string, params any) ([]byte, error) {
	return marshalFrame(Frame{Method: method}, params)
}

// marshalFrame encodes f with params, leaving params out when nil. Params
// that are already JSON (json.RawMessage or []byte) are embedded as-is; any
// other value is marshaled. Re-marshaling a []byte would base64-encode it
// and the server would answer "Params must be an object".
func marshalFrame(f Frame, params any) ([]byte, error) {
	if params != nil {
		switch p := params.(type) {
		case json.RawMessage:
			if len(p) > 0 {
				f.Params = p
			}
		case []byte:
			if len(p) > 0 {
				f.Params = json.RawMessage(p)
			}
		default:
			b, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			f.Params = b
		}
	}
	return json.Marshal(f)
}
