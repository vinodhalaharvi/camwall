package feeds

import "testing"

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"rtsp://admin:hunter2@10.0.0.5:554/live: Connection refused": "rtsp://***@10.0.0.5:554/live: Connection refused",
		"https://cdn.example/v.m3u8?sig=abc&exp=1 failed":            "https://cdn.example/v.m3u8?*** failed",
		"no URL here": "no URL here",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
