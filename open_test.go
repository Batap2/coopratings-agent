package main

import (
	"reflect"
	"testing"
)

// The app window opens on the game asked for by the tray menu or the
// end-of-session notification; anything else shows the home view.
func TestParseOpen(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]any
	}{
		{`identify:c:\jeux\demo\demo.exe`, map[string]any{"kind": "identify", "key": `c:\jeux\demo\demo.exe`}},
		{"rate:1455", map[string]any{"kind": "rate", "itemId": 1455, "seconds": 0}},
		{"session:105:3600", map[string]any{"kind": "session", "itemId": 105, "seconds": 3600}},
		{"", nil},
		{"identify:", nil},
		{"rate:abc", nil},
		{"rate:0", nil},
		{"other:1", nil},
	}
	for _, c := range cases {
		if got := parseOpen(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseOpen(%q) = %v, attendu %v", c.in, got, c.want)
		}
	}
}
