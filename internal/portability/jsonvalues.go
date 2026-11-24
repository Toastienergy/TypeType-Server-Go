package portability

import (
	"encoding/json"
	"strconv"
	"strings"
)

// jstr is PortabilityJsonElements.string: a scalar's text, "" for anything else or a missing key.
func jstr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	return scalarText(m[k])
}

// jlong is PortabilityJsonElements.long: a number, or a string holding one, else 0.
func jlong(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		if f, err := v.Float64(); err == nil {
			return int64(f)
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func jarr(m map[string]any, k string) []any {
	a, _ := m[k].([]any)
	return a
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// eachValue reads the array the decoder is inside, element by element, capped at MaxContainerRecords.
func eachValue(d *json.Decoder, fn func(any) error) error {
	count := 0
	for d.More() {
		if count >= MaxContainerRecords {
			return invalid("Backup section contains too many records")
		}
		count++
		var v any
		if err := d.Decode(&v); err != nil {
			return invalid("Invalid backup section")
		}
		if err := fn(v); err != nil {
			return err
		}
	}
	_, err := d.Token()
	return err
}

// openArray consumes the opening bracket of an array value, or fails with msg.
func openArray(d *json.Decoder, msg string) error {
	t, err := d.Token()
	if err != nil || t != json.Delim('[') {
		return invalid(msg)
	}
	return nil
}
