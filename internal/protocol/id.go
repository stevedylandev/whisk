package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
)

// NewID returns a random opaque request id. Request ids SHOULD be random; they
// identify operations, not log positions.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand is expected to never fail on supported platforms; this
		// fallback keeps ids unique within the process in the face of failure.
		return "id-" + strconv.FormatUint(fallbackSeq.Add(1), 10)
	}
	return "id-" + hex.EncodeToString(b)
}

var fallbackSeq atomic.Uint64
