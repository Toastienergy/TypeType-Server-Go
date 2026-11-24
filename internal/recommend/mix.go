package recommend

import (
	"time"
)

const (
	maxSubscriptionRun = 2
	maxPerChannel      = 2
	maxMomentumPick    = 4
	cooldownThreshold  = 3
	maxMomentum        = 6
	cooldown           = 45 * time.Minute
)

type plan struct {
	targetSub, targetDisc, novelty, floor int
}

// newPlan is HomeRecommendationQuotaPlanner for the home feed, where no source weights apply.
func newPlan(limit int, pool *Pool, intent Intent, persona int) plan {
	subs, disc := len(pool.Subscriptions), len(pool.Discovery)
	theme, explore, trend := 0, 0, 0
	for _, s := range pool.SourceByURL {
		switch s {
		case Theme:
			theme++
		case Exploration:
			explore++
		case Trending:
			trend++
		}
	}
	var ratio float64
	switch {
	case disc == 0:
		ratio = 0
	case subs == 0:
		ratio = 1
	case theme+explore+trend >= subs:
		ratio = 0.65
	case theme+explore >= subs/2:
		ratio = 0.58
	default:
		ratio = 0.50
	}
	switch intent {
	case IntentQuick:
		ratio += 0.08
	case IntentDeep:
		ratio -= 0.08
	}
	switch persona {
	case PersonaQuick:
		ratio += 0.06
	case PersonaDeep:
		ratio -= 0.06
	}
	ratio = max(0.30, min(ratio, 0.80))
	p := plan{targetDisc: min(int(float64(limit)*ratio), disc)}
	p.targetSub = min(limit-p.targetDisc, subs)
	p.floor = min(int(float64(limit)*0.5), disc)
	nr := map[Intent]float64{IntentAuto: 0.25, IntentQuick: 0.35, IntentDeep: 0.20}[intent]
	p.novelty = max(1, min(int(float64(limit)*nr), limit))
	return p
}

type mixState struct {
	subIndex, discIndex, subRun       int
	preferDiscovery                   bool
	subCount, discCount, noveltyCount int
	channelCount                      map[string]int
	channels, semantic, pairs, urls   []string
	momentum                          map[string]int
	cooldown                          map[string]int64
	persona                           Persona
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return append([]string(nil), s...)
}

func push(s []string, v string, n int) []string {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func channelKey(v Video) string {
	if v.UploaderURL != "" {
		return v.UploaderURL
	}
	return v.UploaderName
}

func durationEvidence(p Persona, d int64) Persona {
	q, dp := 0, 0
	if d >= 30 && d <= 420 {
		q = 1
	}
	if d >= 1200 {
		dp = 1
	}
	if q == 0 && dp == 0 {
		return p
	}
	p.Quick, p.Deep = min(p.Quick+q, 8), min(p.Deep+dp, 8)
	switch {
	case p.Deep-p.Quick >= 2:
		p.Kind = PersonaDeep
	case p.Quick-p.Deep >= 2:
		p.Kind = PersonaQuick
	default:
		p.Kind = PersonaAuto
	}
	return p
}

func (m *mixState) selected(v Video, sub bool, novelty bool, now time.Time) {
	if sub {
		m.subCount++
		m.subRun = min(m.subRun+1, maxSubscriptionRun)
		if m.subRun >= maxSubscriptionRun {
			m.preferDiscovery = true
		}
	} else {
		m.discCount++
		m.subRun, m.preferDiscovery = 0, false
	}
	if novelty {
		m.noveltyCount++
	}
	m.persona = durationEvidence(m.persona, v.Duration)
	if k := channelKey(v); k != "" {
		m.channelCount[k]++
		m.channels = push(m.channels, k, 4)
		m.momentum[k] = min(m.momentum[k]+1, maxMomentum)
		if m.momentum[k] >= cooldownThreshold {
			m.cooldown[k] = now.Add(cooldown).UnixMilli()
			m.momentum[k] = cooldownThreshold - 1
		}
	}
	if k := semanticKey(v.Title); k != "" {
		m.semantic = push(m.semantic, k, 5)
	}
	for _, p := range topicPairs(v.Title) {
		m.pairs = push(m.pairs, p, 14)
	}
	if v.URL != "" {
		m.urls = push(m.urls, v.URL, 80)
	}
}

func set(s []string) map[string]bool {
	out := make(map[string]bool, len(s))
	for _, v := range s {
		out[v] = true
	}
	return out
}

// pick walks a list from start and returns the first video the diversity rules allow, with the next index.
func (m *mixState) pick(list []Video, start int, noveltyOnly bool, now time.Time) (*Video, int) {
	urls, chans, sem, pairs := set(m.urls), set(m.channels), set(m.semantic), set(m.pairs)
	i := start
	for i < len(list) {
		c := list[i]
		i++
		key := semanticKey(c.Title)
		if urls[c.URL] || noveltyOnly && (key == "" || sem[key]) {
			continue
		}
		ch := channelKey(c)
		if ch == "" {
			return &c, i
		}
		if m.channelCount[ch] >= maxPerChannel || chans[ch] || m.cooldown[ch] > now.UnixMilli() || m.momentum[ch] >= maxMomentumPick {
			continue
		}
		if key != "" && sem[key] {
			continue
		}
		clash := false
		for _, p := range topicPairs(c.Title) {
			if pairs[p] {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		return &c, i
	}
	return nil, i
}

func (m *mixState) pickRelaxed(list []Video, start int) (*Video, int) {
	for i := start; i < len(list); i++ {
		c := list[i]
		if ch := channelKey(c); ch != "" && m.channelCount[ch] >= maxPerChannel {
			continue
		}
		return &c, i + 1
	}
	return nil, len(list)
}

func (m *mixState) pickDiscovery(pool *Pool, noveltyOnly bool, now time.Time) (*Video, int) {
	if v, i := m.pick(pool.Discovery, m.discIndex, noveltyOnly, now); v != nil {
		return v, i
	}
	return m.pickRelaxed(pool.Discovery, m.discIndex)
}

// Page is one mixed page and the cursor to the next one (empty when nothing is left).
type Page struct {
	Items      []Video
	NextCursor string
}

// Mix is HomeRecommendationMixEngine: subscriptions and discovery interleaved under quotas, with no channel more than
// twice a page, no channel twice in a row, no repeated topic, and a novelty budget.
func Mix(pool *Pool, c Cursor, limit int, intent Intent, now time.Time) Page {
	p := newPlan(limit, pool, intent, c.Persona.Kind)
	m := &mixState{subIndex: c.SubIndex, discIndex: c.DiscIndex, subRun: max(0, min(c.SubRun, maxSubscriptionRun)), preferDiscovery: c.PreferDiscovery,
		channelCount: map[string]int{}, channels: lastN(c.RecentChannels, 4), semantic: lastN(c.RecentSemantic, 5), pairs: lastN(c.TopicPairs, 14),
		urls: lastN(c.RecentURLs, 80), momentum: map[string]int{}, cooldown: map[string]int64{}, persona: c.Persona}
	for k, v := range c.Momentum {
		m.momentum[k] = v
	}
	for k, v := range c.Cooldown {
		m.cooldown[k] = v
	}
	switch intent {
	case IntentQuick:
		m.persona = Persona{Kind: PersonaQuick, Quick: max(c.Persona.Quick, 1), Deep: c.Persona.Deep}
	case IntentDeep:
		m.persona = Persona{Kind: PersonaDeep, Quick: c.Persona.Quick, Deep: max(c.Persona.Deep, 1)}
	}
	var items []Video
	for len(items) < limit {
		sel := len(items)
		remaining := limit - sel
		force := max(0, p.floor-m.discCount) >= remaining || max(0, p.targetDisc-m.discCount) >= remaining || m.subCount >= p.targetSub
		want := force || m.subRun >= maxSubscriptionRun || m.discCount < p.targetDisc || m.preferDiscovery
		forceNovelty := max(0, p.novelty-m.noveltyCount) >= remaining
		var v *Video
		sub, novelty := false, false
		if want {
			var i int
			if v, i = m.pickDiscovery(pool, forceNovelty, now); v != nil {
				m.discIndex, novelty = i, true
			} else if !force {
				if v, i = m.pick(pool.Subscriptions, m.subIndex, false, now); v != nil {
					m.subIndex, sub = i, true
				}
			}
		} else {
			var i int
			if v, i = m.pick(pool.Subscriptions, m.subIndex, false, now); v != nil {
				m.subIndex, sub = i, true
			} else if v, i = m.pickDiscovery(pool, false, now); v != nil {
				m.discIndex, novelty = i, true
			}
		}
		if v == nil {
			break
		}
		items = append(items, *v)
		isSub := sub
		if src, ok := pool.SourceByURL[v.URL]; ok {
			isSub = src == Subscription
		}
		m.selected(*v, isSub, novelty, now)
	}
	out := Page{Items: items}
	if len(items) > 0 && (m.subIndex < len(pool.Subscriptions) || m.discIndex < len(pool.Discovery)) {
		out.NextCursor = Cursor{SubIndex: m.subIndex, DiscIndex: m.discIndex, SubRun: m.subRun, PreferDiscovery: m.preferDiscovery, Seed: c.Seed,
			RecentChannels: m.channels, RecentSemantic: m.semantic, Momentum: m.momentum, Cooldown: m.cooldown, TopicPairs: m.pairs,
			RecentURLs: m.urls, Persona: m.persona}.Encode()
	}
	return out
}
