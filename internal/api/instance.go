package api

import (
	"net/http"
	"os"
	"strings"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

type rssCapability struct {
	Enabled            bool `json:"enabled"`
	MaxFeedsPerUser    int  `json:"maxFeedsPerUser"`
	MaxItems           int  `json:"maxItems"`
	MinimumPollMinutes int  `json:"minimumPollMinutes"`
	RateLimitPerMinute int  `json:"rateLimitPerMinute"`
}

type pushCapability struct {
	Enabled              bool     `json:"enabled"`
	Provider             string   `json:"provider"`
	EventTypes           []string `json:"eventTypes"`
	MaxDevicesPerAccount int      `json:"maxDevicesPerAccount"`
}

type recommenderCapability struct {
	Installed    bool `json:"installed"`
	Enabled      bool `json:"enabled"`
	Experimental bool `json:"experimental"`
}

type instanceResponse struct {
	Name                                string                `json:"name"`
	Tagline                             *string               `json:"tagline"`
	Version                             string                `json:"version"`
	Revision                            string                `json:"revision"`
	ShortRevision                       string                `json:"shortRevision"`
	BuildTime                           string                `json:"buildTime"`
	APIVersion                          int                   `json:"apiVersion"`
	RegistrationAllowed                 bool                  `json:"registrationAllowed"`
	GuestAllowed                        bool                  `json:"guestAllowed"`
	SupportedServices                   []int                 `json:"supportedServices"`
	MinClientVersion                    map[string]*string    `json:"minClientVersion"`
	LogoURL                             *string               `json:"logoUrl"`
	BannerURL                           *string               `json:"bannerUrl"`
	LocalLoginEnabled                   bool                  `json:"localLoginEnabled"`
	OidcEnabled                         bool                  `json:"oidcEnabled"`
	OidcProviderName                    *string               `json:"oidcProviderName"`
	OidcAutoRedirect                    bool                  `json:"oidcAutoRedirect"`
	YoutubeRemoteLoginEnabled           bool                  `json:"youtubeRemoteLoginEnabled"`
	YoutubeRemoteLoginReady             bool                  `json:"youtubeRemoteLoginReady"`
	YoutubeRemoteLoginUnavailableReason *string               `json:"youtubeRemoteLoginUnavailableReason"`
	ParentalControlsEnabled             bool                  `json:"parentalControlsEnabled"`
	Rss                                 rssCapability         `json:"rss"`
	PushNotifications                   pushCapability        `json:"pushNotifications"`
	Recommender                         recommenderCapability `json:"recommender"`
	Twitch                              struct {
		Enabled bool `json:"enabled"`
	} `json:"twitch"`
	DeviceLoginEnabled bool `json:"deviceLoginEnabled"`
}

// Features read once from the environment, the way the Kotlin services decide them at startup.
type features struct {
	oidcName       string
	recommenderURL string
	twitchEnabled  bool
	pushEnabled    bool
}

func loadFeatures(cfg *config.Config) features {
	f := features{recommenderURL: config.TrimmedURL("RECOMMENDER_SERVICE_URL"), pushEnabled: cfg.PushEnabled}
	issuer := strings.TrimRight(config.Nonblank("OIDC_ISSUER", "OIDC_ISSUER_URL"), "/")
	if issuer != "" && strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")) != "" && strings.TrimSpace(os.Getenv("OIDC_CLIENT_SECRET")) != "" {
		f.oidcName = strings.TrimSpace(os.Getenv("OIDC_PROVIDER_NAME"))
		if f.oidcName == "" {
			f.oidcName = "OIDC"
		}
	}
	key := config.Secret("TWITCH_TOKEN_ENCRYPTION_KEY")
	f.twitchEnabled = config.TrimmedURL("TWITCH_SERVICE_URL") != "" && config.Secret("TWITCH_SERVICE_SECRET") != "" && len(key) >= 32
	return f
}

func (s *Server) instance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Admin.Get(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hasAdmin, err := s.Auth.HasAdmin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := instanceResponse{
		Name: st.Name, Tagline: st.Tagline, Version: Version, Revision: Revision, ShortRevision: ShortRevision,
		BuildTime: BuildTime, APIVersion: 1, RegistrationAllowed: st.AllowRegistration || !hasAdmin,
		GuestAllowed: st.AllowGuest, SupportedServices: []int{0, 5, 6},
		MinClientVersion: map[string]*string{"android": st.MinAndroidClientVersion},
		LogoURL:          st.LogoURL, BannerURL: st.BannerURL, LocalLoginEnabled: st.LocalLoginEnabled,
		OidcEnabled: s.features.oidcName != "", OidcAutoRedirect: s.features.oidcName != "" && st.OidcAutoRedirect,
		ParentalControlsEnabled: st.AccessMode == store.AccessAllowList, DeviceLoginEnabled: st.DeviceLoginEnabled,
		Rss: rssCapability{Enabled: st.RssEnabled && st.RssPublicBaseURL != nil, MaxFeedsPerUser: st.RssMaxFeedsPerUser,
			MaxItems: st.RssMaxItems, MinimumPollMinutes: st.RssMinimumPollMinutes, RateLimitPerMinute: st.RssRateLimitPerMinute},
		PushNotifications: pushCapability{Enabled: s.features.pushEnabled, Provider: "unifiedpush",
			EventTypes: []string{"subscription_new_video"}, MaxDevicesPerAccount: 8},
		Recommender: recommenderCapability{Installed: s.features.recommenderURL != "",
			Enabled: s.features.recommenderURL != "" && st.RecommenderEnabled, Experimental: true},
	}
	if s.features.oidcName != "" {
		resp.OidcProviderName = &s.features.oidcName
	}
	reason := "disabled"
	if st.YoutubeRemoteLoginEnabled {
		reason = "not_configured"
	}
	resp.YoutubeRemoteLoginUnavailableReason = &reason
	resp.Twitch.Enabled = s.features.twitchEnabled
	w.Header().Set("Cache-Control", instanceCacheControl)
	httpx.JSON(w, http.StatusOK, resp)
}
