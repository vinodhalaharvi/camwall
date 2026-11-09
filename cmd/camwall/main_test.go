package main

import (
	"testing"
	"time"
)

func TestLocalHost(t *testing.T) {
	for h, want := range map[string]bool{
		"127.0.0.1:7788": true, "[::1]:7788": true, "localhost:7788": true, "192.168.1.20:7788": true,
		"shop-pc:7788": true, "shop-pc.local": true, "evil.example.com:7788": false, "rebind.attacker.net": false,
	} {
		if localHost(h) != want {
			t.Errorf("localHost(%q) = %v", h, !want)
		}
	}
}

func TestParseKeep(t *testing.T) {
	for _, bad := range []string{"NaNd", "-1d", "1e300d", "Infd", "x"} {
		if _, err := parseKeep(bad); err == nil {
			t.Errorf("parseKeep(%q) accepted", bad)
		}
	}
	if d, err := parseKeep("7d"); err != nil || d != 7*24*time.Hour {
		t.Errorf("7d: %v %v", d, err)
	}
}
