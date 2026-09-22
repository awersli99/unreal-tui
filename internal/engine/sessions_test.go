package engine

import (
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/session"
)

func TestPickSession(t *testing.T) {
	sessions := []SessionSummary{{ID: "12345678-aaaa"}, {ID: "abcdef01-bbbb"}, {ID: "abcdef02-cccc"}}
	for selector, want := range map[string]session.ID{
		"2":        "abcdef01-bbbb",
		"12345678": "12345678-aaaa", // An all-digit ID prefix, not a list number.
		"abcdef02": "abcdef02-cccc",
	} {
		if got, err := PickSession(sessions, selector); err != nil || got != want {
			t.Errorf("pickSession(%q) = %q, %v; want %q", selector, got, err, want)
		}
	}
	for _, selector := range []string{"abcdef", "9", "zzz"} {
		if got, err := PickSession(sessions, selector); err == nil {
			t.Errorf("pickSession(%q) = %q, want an error", selector, got)
		}
	}
}
