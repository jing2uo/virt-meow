package hv

import (
	"slices"
	"testing"
)

func TestParseKeys(t *testing.T) {
	for in, want := range map[string][]uint32{
		"ctrl+alt+delete": {29, 56, 111},
		"Ctrl+Alt+F2":     {29, 56, 60},
		"a":               {30},
		"KEY_ENTER":       {28},
		"shift+1":         {42, 2},
		"0":               {11},
		"f12":             {88},
		"m":               {50},
		"125":             {125},
	} {
		if got, err := ParseKeys(in); err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+", "hyper", "ctrl+alt+f13", "99999"} {
		if got, err := ParseKeys(bad); err == nil {
			t.Errorf("%q: %v, want an error", bad, got)
		}
	}
}
