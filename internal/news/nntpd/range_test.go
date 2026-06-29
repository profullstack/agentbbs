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

func TestParseRangeClosed(t *testing.T) {
	low, high := parseRange("5-10")
	if low != 5 || high != 10 {
		t.Fatalf(`parseRange("5-10") = %d, %d; want 5, 10`, low, high)
	}
}

// "low-" is a valid open-ended range meaning "from low to the highest
// article" (RFC 3977). It must not be treated as a malformed empty range,
// otherwise OVER/XOVER "n-" returns nothing instead of all articles >= n.
func TestParseRangeOpenEnded(t *testing.T) {
	low, high := parseRange("5-")
	if low != 5 || high != math.MaxInt64 {
		t.Fatalf(`parseRange("5-") = %d, %d; want 5, %d`, low, high, int64(math.MaxInt64))
	}
}

// Empty spec means "all articles".
func TestParseRangeEmptyIsAll(t *testing.T) {
	low, high := parseRange("")
	if low != 0 || high != math.MaxInt64 {
		t.Fatalf(`parseRange("") = %d, %d; want 0, %d`, low, high, int64(math.MaxInt64))
	}
}

// Malformed specs must yield an empty range (0,0), never "all articles".
func TestParseRangeMalformed(t *testing.T) {
	for _, spec := range []string{"abc", "5-abc", "abc-5", "1-2-3", "-", "-5"} {
		low, high := parseRange(spec)
		if low != 0 || high != 0 {
			t.Fatalf(`parseRange(%q) = %d, %d; want 0, 0`, spec, low, high)
		}
	}
}
