package api

import (
	"net/http"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

const providerBlockedMessage = "The video provider is blocking requests from this TypeType server. Try a VPN or another outbound network for the server, then retry."

// writeRestriction answers a YouTube refusal as extractionFailureStatus and extractionBadRequestStatus do for
// YouTube: content restrictions 403, gone content 404, lives and premieres not started 409, bot checks 503.
func writeRestriction(w http.ResponseWriter, r *http.Request, rs *youtube.Restriction) {
	status, msg := http.StatusForbidden, rs.Message
	switch rs.Code {
	case "provider_access_blocked":
		status, msg = http.StatusServiceUnavailable, providerBlockedMessage
		w.Header().Set("Retry-After", "60")
	case "content_unavailable":
		status = http.StatusNotFound
	case "live_not_started", "scheduled_premiere":
		status = http.StatusConflict
	}
	httpx.Error(w, r, status, msg, rs.Code)
}
