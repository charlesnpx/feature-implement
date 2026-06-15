package plan

import (
	"regexp"
	"strconv"
	"strings"
)

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

func num(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat("0", max(0, 3-len(strconv.Itoa(n)))) + strconv.Itoa(n)
}
