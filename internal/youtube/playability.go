package youtube

import "strings"

// Restriction is why YouTube refused to play a video, with the code TypeType's API answers for it.
type Restriction struct {
	Code, Message string
}

const (
	membersOnly    = "This video is only available for members"
	signInRequired = "Sign in is required to verify access to this video"
)

// CheckPlayability is YoutubeStreamExtractor.checkPlayabilityStatus followed by StreamExtractionErrorMapper and
// ExtractionErrorSanitizer: nil when the status is OK or absent.
func CheckPlayability(ps map[string]any) *Restriction {
	status := Str(ps, "status")
	if ps == nil || status == "" || strings.EqualFold(status, "ok") {
		return nil
	}
	reason := Str(ps, "reason")
	if Obj(ps, "errorScreen", "playerCaptchaViewModel") != nil {
		return &Restriction{"provider_access_blocked", ""}
	}
	if strings.EqualFold(status, "login_required") {
		if reason == "" {
			if m, _ := Dig(ps, "messages", 0).(string); strings.Contains(m, "private") {
				return sanitized("private_content", "This video is private")
			}
		}
		if strings.Contains(reason, "age") {
			return sanitized("age_restricted", "This age-restricted video cannot be watched anonymously")
		}
	}
	if (strings.EqualFold(status, "unplayable") || strings.EqualFold(status, "error")) && reason != "" {
		switch {
		case strings.Contains(reason, "Music Premium"):
			return &Restriction{"paid_content", "This video is a paid video"}
		case strings.Contains(reason, "payment"):
			return paid("This video is a paid video")
		case strings.Contains(reason, "members-only"):
			return paid("This video is only available for members of the channel of this video")
		case strings.Contains(reason, "unavailable"):
			detail := Text(Obj(ps, "errorScreen", "playerErrorMessageRenderer", "subreason"))
			switch {
			case strings.Contains(detail, "country"):
				return sanitized("geographic_restriction", "This video is not available in client's country.")
			case detail != "":
				return sanitized("content_unavailable", detail)
			}
			return sanitized("content_unavailable", reason)
		}
	}
	switch {
	case strings.Contains(reason, "Sign in to confirm"):
		return &Restriction{"provider_access_blocked", ""}
	case strings.Contains(reason, "This live event will begin in"):
		return sanitized("live_not_started", reason)
	case strings.Contains(reason, "Premieres in"):
		return sanitized("scheduled_premiere", reason)
	}
	return sanitized("content_unavailable", `Got error: "`+orNull(reason)+`"`)
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

func paid(msg string) *Restriction {
	if r := sanitized("paid_content", msg); r.Message != membersOnly {
		return r
	}
	return &Restriction{"members_only", membersOnly}
}

func sanitized(code, raw string) *Restriction {
	msg := strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(raw))
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "members") && strings.Contains(l, "only available"),
		strings.Contains(l, "join this channel") && strings.Contains(l, "members-only"):
		msg = membersOnly
	case strings.Contains(l, "sign in to confirm"), strings.Contains(l, "not a bot"), strings.Contains(l, "qinisekisa ukuthi"),
		strings.Contains(l, "awuyona i-bot"), strings.Contains(l, "ngena ngemvume"):
		msg = signInRequired
	}
	return &Restriction{code, msg}
}
