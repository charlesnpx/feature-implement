package plan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const safeIDPattern = `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlug.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "untitled"
	}
	return value
}

func validateSafeIDSegment(id string, kind string) error {
	if matched, _ := regexp.MatchString(safeIDPattern, id); !matched {
		return fmt.Errorf("%s id %q must contain only lowercase letters, numbers, and hyphen separators", kind, id)
	}
	return nil
}

func num(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat("0", max(0, 3-len(strconv.Itoa(n)))) + strconv.Itoa(n)
}
