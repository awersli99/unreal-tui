package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/session"
)

// ShortID is the prefix of a session ID shown to users and accepted by
// --session and /resume.
func ShortID(id session.ID) string {
	return string(id)[:min(8, len(id))]
}

// PickSession finds a session by its number in the list (1 is the most
// recent) or by a unique ID prefix. A number outside the list is treated as a
// prefix, since short IDs can be all digits.
func PickSession(sessions []SessionSummary, selector string) (session.ID, error) {
	if number, err := strconv.Atoi(selector); err == nil && number >= 1 && number <= len(sessions) {
		return sessions[number-1].ID, nil
	}
	var match session.ID
	for _, summary := range sessions {
		if strings.HasPrefix(string(summary.ID), selector) {
			if match != "" {
				return "", fmt.Errorf("session prefix %q is ambiguous", selector)
			}
			match = summary.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("no session matches %q", selector)
	}
	return match, nil
}
