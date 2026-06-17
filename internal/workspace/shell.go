package workspace

import "fmt"

var SupportedActions = []string{"init", "validate", "status", "next", "heartbeat", "release", "recover", "attempt"}

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
