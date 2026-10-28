package protocol

import "testing"

func TestLogIDAfter(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10", "9", true},
		{"9", "10", false},
		{"5", "5", false},
		{"", "5", false},
		{"5", "", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := LogIDAfter(c.a, c.b); got != c.want {
			t.Errorf("LogIDAfter(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNextAndPrevLogID(t *testing.T) {
	if got := NextLogID("41"); got != "42" {
		t.Errorf("NextLogID(41) = %q", got)
	}
	if got := NextLogID(""); got != "0" {
		t.Errorf("NextLogID(\"\") = %q", got)
	}
	if got := PrevLogID("42"); got != "41" {
		t.Errorf("PrevLogID(42) = %q", got)
	}
	if got := PrevLogID("0"); got != "" {
		t.Errorf("PrevLogID(0) = %q", got)
	}
}
