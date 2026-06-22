package source

import (
	"net"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		raw     string
		want    Kind
		wantErr bool
	}{
		{"https://youtube.com/live/abc123", KindYouTube, false},
		{"https://www.youtube.com/watch?v=abc", KindYouTube, false},
		{"https://youtu.be/abc123", KindYouTube, false},
		{"https://example.com/path/stream.m3u8", KindHLS, false},
		{"http://cdn.example.com/live.m3u8?token=x", KindHLS, false},
		{"https://example.com/whatever", KindHLS, false}, // unknown http(s) → HLS handling
		{"file:///etc/passwd", "", true},
		{"rtmp://example.com/live", "", true},
		{"ftp://example.com/x", "", true},
	}
	for _, c := range cases {
		got, err := Classify(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("Classify(%q): expected error, got kind %q", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Classify(%q): unexpected error: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.0.0.5",        // private
		"172.16.3.4",      // private
		"192.168.1.1",     // private
		"169.254.169.254", // link-local / cloud metadata
		"100.100.100.200", // shared space, commonly used by cloud metadata services
		"198.18.0.1",      // benchmarking range
		"192.0.2.10",      // TEST-NET-1
		"198.51.100.10",   // TEST-NET-2
		"203.0.113.10",    // TEST-NET-3
		"fe80::1",         // link-local v6
		"fc00::1",         // unique-local v6 (private)
		"2001:db8::1",     // documentation v6
		"2002::1",         // 6to4
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want true", s)
		}
	}

	allowed := []string{
		"8.8.8.8",              // public
		"1.1.1.1",              // public
		"142.250.72.46",        // public (googlevideo-ish)
		"2606:4700:4700::1111", // public v6
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = true, want false", s)
		}
	}
}
