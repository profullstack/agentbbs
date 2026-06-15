package nntpd

import "testing"

func TestParseRangeSingleArticle(t *testing.T) {
	low, high := parseRange("5")
	if low != 5 || high != 5 {
		t.Fatalf(`parseRange("5") = %d, %d; want 5, 5`, low, high)
	}
}
