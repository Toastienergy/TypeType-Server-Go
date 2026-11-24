package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

type ctxKey int

const requestIDKey ctxKey = iota

func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// ErrorBody is the Kotlin ErrorResponse: error, code and the request id, always present.
type ErrorBody struct {
	Error     string  `json:"error"`
	Code      string  `json:"code"`
	RequestID *string `json:"requestId"`
}

// marshal leaves <, > and & as they are, like kotlinx.serialization; only the API reads these bodies.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func JSON(w http.ResponseWriter, status int, v any) {
	b, err := marshal(v)
	if err != nil {
		Error(w, nil, http.StatusInternalServerError, "Internal server error", "internal_error")
		return
	}
	RawJSON(w, status, b)
}

func RawJSON(w http.ResponseWriter, status int, b []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func Error(w http.ResponseWriter, r *http.Request, status int, msg, code string) {
	body := ErrorBody{Error: msg, Code: code}
	if r != nil {
		if id := RequestID(r.Context()); id != "" {
			body.RequestID = &id
		}
	}
	b, _ := marshal(body)
	RawJSON(w, status, b)
}

func BadRequest(w http.ResponseWriter, r *http.Request, msg string) {
	Error(w, r, http.StatusBadRequest, msg, "bad_request")
}

func NotFound(w http.ResponseWriter, r *http.Request, msg string) {
	Error(w, r, http.StatusNotFound, msg, "not_found")
}

func Unauthorized(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusUnauthorized, "Unauthorized", "unauthorized")
}
