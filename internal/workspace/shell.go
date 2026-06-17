package workspace

import (
	"fmt"
	"strings"
)

var SupportedActions = []string{"init", "validate", "status", "next", "heartbeat", "release", "recover", "attempt", "transition"}

func IsSupportedAction(action string) bool {
	for _, supported := range SupportedActions {
		if action == supported {
			return true
		}
	}
	return false
}

func ErrNotImplemented(action string) error {
	return fmt.Errorf("workspace %s is not implemented yet", action)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '/' ||
			r == '.' ||
			r == '_' ||
			r == '-' ||
			r == ':' ||
			r == '@' ||
			r == '+')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
