package store

import (
	"strconv"
	"strings"
)

func itoa(i int) string { return strconv.Itoa(i) }

func splitCols(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
