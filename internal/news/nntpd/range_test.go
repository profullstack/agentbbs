package nntpd

import (
	"math"
	"testing"
)

func TestParseRangeSingleArticle(t *testing.T) {
	low, high := parseRange("5")
	if low != 5 || high != 5 {
		t.Fatalf(`parseRange("5") = %d, %d; want 5, 5`, low, high)
	}
}

func TestParseRange(t *testing.T) {
	// spec -> (low, high). Empty bounds mean the extreme; malformed -> (0, 0).
	cases := []struct {
		spec string
		low  int64
		high int64
	}{
		{"5", 5, 5},
		{"5-10", 5, 10},
		{"5-", 5, math.MaxInt64},
		{"-10", 0, 10},
		{"", 0, math.MaxInt64},
		{"abc", 0, 0},
		{"5-abc", 0, 0},
		{"abc-10", 0, 0},
	}
	for _, c := range cases {
		low, high := parseRange(c.spec)
		if low != c.low || high != c.high {
			t.Errorf("parseRange(%q) = %d, %d; want %d, %d", c.spec, low, high, c.low, c.high)
		}
	}
}
