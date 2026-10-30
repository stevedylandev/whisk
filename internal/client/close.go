package client

import (
	"errors"
	"fmt"
)

// CloseInfo carries a peer close's status code and reason. The limits doc's
// closed-connections table tells what each code means: among others 1001
// stale, 1008 a policy or credential close, 1009 an oversized frame, 1011
// delivery trouble, and 1013 an address frame-rate or budget close.
type CloseInfo struct {
	Code   int
	Reason string
}

// AsClose reports whether a disconnect was ended by a clean peer close, and
// fills info with its status code and reason.
func AsClose(err error, info *CloseInfo) bool {
	var d *disconnectedError
	if !errors.As(err, &d) {
		return false
	}
	*info = d.CloseInfo
	return true
}

// disconnectedError is a transport failure, optionally carrying a close
// frame's status code and reason. The underlying error is kept, so
// errors.Unwrap reaches it.
type disconnectedError struct {
	cause error
	CloseInfo
}

func (e *disconnectedError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s (code %d: %s)", e.cause, e.Code, e.Reason)
	}
	return e.cause.Error()
}

func (e *disconnectedError) Unwrap() error { return e.cause }

// NewClose builds a disconnect error carrying a close's status code and
// reason, as the peer sent. Tests and callers that simulate a close go
// through it; readLoop wraps the live read error instead.
func NewClose(cause error, code int, reason string) error {
	return &disconnectedError{
		cause: cause,
		CloseInfo: CloseInfo{
			Code:   code,
			Reason: reason,
		},
	}
}
