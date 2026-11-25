package recommender

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// Feedback is RecommenderFeedbackItem: an opinion on a whole home page, or a video marked not for me.
type Feedback struct {
	ID           string                     `json:"id"`
	Kind         string                     `json:"kind"`
	Verdict      string                     `json:"verdict"`
	Comment      *string                    `json:"comment"`
	VideoURL     *string                    `json:"videoUrl"`
	Reason       *string                    `json:"reason"`
	Title        *string                    `json:"title"`
	ChannelName  *string                    `json:"channelName"`
	ThumbnailURL *string                    `json:"thumbnailUrl"`
	AlgorithmID  string                     `json:"algorithmId"`
	Parameters   map[string]json.RawMessage `json:"parameters"`
	CreatedAt    int64                      `json:"createdAt"`
}

const (
	KindPage        = "page"
	KindVideo       = "video"
	verdictNotForMe = "not_for_me"
	verdictBad      = "bad"
	maxTitleChars   = 300
	maxCommentChars = 1_000
	maxURLChars     = 500
)

var (
	pageVerdicts = []string{"good", verdictBad}
	pageReasons  = []string{"repetitive", "unrelated", "old", "missing"}
	videoReasons = []string{"seen", "channel", "topic", "other"}
)

// FeedbackService is RecommenderFeedbackService.
type FeedbackService struct {
	db               Querier
	settings         *SettingsService
	defaultAlgorithm func(ctx context.Context) string
}

func NewFeedbackService(db Querier, settings *SettingsService, defaultAlgorithm func(ctx context.Context) string) *FeedbackService {
	return &FeedbackService{db: db, settings: settings, defaultAlgorithm: defaultAlgorithm}
}

func (f *FeedbackService) List(ctx context.Context, user string) ([]Feedback, error) {
	rows, err := f.db.Query(ctx, `SELECT id, kind, verdict, comment, video_url, reason, title, channel_name, thumbnail_url,
algorithm_id, parameters_json, created_at FROM recommender_feedback WHERE user_id = $1 ORDER BY created_at DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var it Feedback
		var params string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Verdict, &it.Comment, &it.VideoURL, &it.Reason, &it.Title, &it.ChannelName,
			&it.ThumbnailURL, &it.AlgorithmID, &params, &it.CreatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(params), &it.Parameters) != nil || it.Parameters == nil {
			it.Parameters = map[string]json.RawMessage{}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Add stores an opinion; nil when the kind or verdict is not one Kotlin accepts. A video already marked keeps its
// row and only takes the new reason.
func (f *FeedbackService) Add(ctx context.Context, user string, req Feedback) (*Feedback, error) {
	videoURL := ""
	if req.VideoURL != nil {
		videoURL = strings.TrimSpace(*req.VideoURL)
	}
	video := req.Kind == KindVideo
	switch {
	case req.Kind == KindPage && slices.Contains(pageVerdicts, req.Verdict):
	case video && req.Verdict == verdictNotForMe && videoURL != "":
	default:
		return nil, nil
	}
	var reason *string
	if req.Reason != nil && (video && slices.Contains(videoReasons, *req.Reason) ||
		!video && req.Verdict == verdictBad && slices.Contains(pageReasons, *req.Reason)) {
		reason = req.Reason
	}
	if video {
		all, err := f.List(ctx, user)
		if err != nil {
			return nil, err
		}
		for _, known := range all {
			if known.Kind != KindVideo || known.VideoURL == nil || *known.VideoURL != videoURL {
				continue
			}
			if reason == nil || known.Reason != nil && *known.Reason == *reason {
				return &known, nil
			}
			if _, err := f.db.Exec(ctx, "UPDATE recommender_feedback SET reason = $1 WHERE user_id = $2 AND id = $3", *reason, user, known.ID); err != nil {
				return nil, err
			}
			known.Reason = reason
			return &known, nil
		}
	}
	st, err := f.settings.Get(ctx, user)
	if err != nil {
		return nil, err
	}
	algorithm := ""
	if st.AlgorithmID != nil {
		algorithm = *st.AlgorithmID
	} else {
		algorithm = f.defaultAlgorithm(ctx)
	}
	item := Feedback{ID: newID(), Kind: req.Kind, Verdict: req.Verdict, Reason: reason, AlgorithmID: algorithm,
		Parameters: st.Parameters, CreatedAt: time.Now().UnixMilli()}
	if req.Comment != nil {
		if c := truncate(strings.TrimSpace(*req.Comment), maxCommentChars); c != "" {
			item.Comment = &c
		}
	}
	if video {
		u := truncate(videoURL, maxURLChars)
		item.VideoURL = &u
		item.Title = shown(req.Title, maxTitleChars)
		item.ChannelName = shown(req.ChannelName, maxTitleChars)
		item.ThumbnailURL = shown(req.ThumbnailURL, maxURLChars)
	}
	params, _ := json.Marshal(item.Parameters)
	if _, err := f.db.Exec(ctx, `INSERT INTO recommender_feedback (id, user_id, kind, verdict, comment, video_url, reason, title,
channel_name, thumbnail_url, algorithm_id, parameters_json, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT DO NOTHING`, item.ID, user, item.Kind, item.Verdict, item.Comment, item.VideoURL, item.Reason, item.Title,
		item.ChannelName, item.ThumbnailURL, item.AlgorithmID, string(params), item.CreatedAt); err != nil {
		return nil, err
	}
	return &item, nil
}

// Delete removes an opinion and returns it; nil when the user has none with that id.
func (f *FeedbackService) Delete(ctx context.Context, user, id string) (*Feedback, error) {
	all, err := f.List(ctx, user)
	if err != nil {
		return nil, err
	}
	for _, it := range all {
		if it.ID != id {
			continue
		}
		tag, err := f.db.Exec(ctx, "DELETE FROM recommender_feedback WHERE user_id = $1 AND id = $2", user, id)
		if err != nil || tag.RowsAffected() == 0 {
			return nil, err
		}
		return &it, nil
	}
	return nil, nil
}

// NotForMeURLs are the videos the user turned down, sent to the service so it never offers them again.
func (f *FeedbackService) NotForMeURLs(ctx context.Context, user string) []string {
	all, _ := f.List(ctx, user)
	var out []string
	for _, it := range all {
		if it.Kind == KindVideo && it.VideoURL != nil && !slices.Contains(out, *it.VideoURL) {
			out = append(out, *it.VideoURL)
		}
	}
	return out
}

func shown(v *string, max int) *string {
	if v == nil {
		return nil
	}
	s := truncate(strings.TrimSpace(*v), max)
	if s == "" {
		return nil
	}
	return &s
}

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
