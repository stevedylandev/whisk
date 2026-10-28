package protocol

import "strconv"

// Log ids and message ids are decimal log positions (§2.3). These helpers
// compare and step them.

// LogIDAfter reports whether log id a is greater than b. An empty a is never
// after anything, and any non-empty a is after an empty b. Ids that do not
// parse as decimals fall back to string comparison.
func LogIDAfter(a, b string) bool {
	if a == b || a == "" {
		return false
	}
	if b == "" {
		return true
	}
	ai, aerr := strconv.ParseUint(a, 10, 64)
	bi, berr := strconv.ParseUint(b, 10, 64)
	if aerr == nil && berr == nil {
		return ai > bi
	}
	return a > b
}

// NextLogID returns id+1, the exclusive lower bound that continues a forward
// page (§4.3.2: after = last_log_id + 1). It is "0" when id is not a number.
func NextLogID(id string) string {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return "0"
	}
	return strconv.FormatUint(n+1, 10)
}

// PrevLogID returns id-1, the before bound that pages backward from id. It is
// "" when id is not a positive number.
func PrevLogID(id string) string {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n-1, 10)
}
