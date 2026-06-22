package nntpd

import (
	"math"
	"testing"
)

func TestParseRangeSingleArticle(t *testing.T) {
	low, high, ok := parseRange("5")
	if !ok || low != 5 || high != 5 {
		t.Fatalf(`parseRange("5") = %d, %d, %v; want 5, 5, true`, low, high, ok)
	}
}

func TestParseRangeOpenEnded(t *testing.T) {
	low, high, ok := parseRange("5-")
	if !ok || low != 5 || high != math.MaxInt64 {
		t.Fatalf(`parseRange("5-") = %d, %d, %v; want 5, MaxInt64, true`, low, high, ok)
	}
}

func TestParseRangeRejectsMalformed(t *testing.T) {
	for _, spec := range []string{"abc", "1-abc", "-5", "10-5", "1-2-3"} {
		if low, high, ok := parseRange(spec); ok {
			t.Fatalf("parseRange(%q) = %d, %d, true; want invalid", spec, low, high)
		}
	}
}
