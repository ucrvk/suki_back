package lib

import "strings"

func shortLogValue(value string, keep int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<empty>"
	}
	if keep <= 0 || len(value) <= keep {
		return value
	}
	return value[:keep] + "…"
}
