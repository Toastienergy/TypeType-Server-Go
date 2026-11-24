package portability

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The vocabulary YouTube Takeout uses in its file names, CSV headers and activity entries, in the languages the
// Kotlin server knows, compared after takeoutNorm.

var nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// takeoutNorm is YoutubeTakeoutTextNormalizer: lower case, accents removed, every run of other characters a space.
func takeoutNorm(v string) string {
	d := norm.NFD.String(strings.ToLower(v))
	var b strings.Builder
	b.Grow(len(d))
	for _, r := range d {
		if !unicode.Is(unicode.M, r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(nonAlnum.ReplaceAllString(b.String(), " "))
}

func normSet(words ...string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = takeoutNorm(w)
	}
	return out
}

func containsAnyNorm(normalized string, words []string) bool {
	for _, w := range words {
		if strings.Contains(normalized, w) {
			return true
		}
	}
	return false
}

func inSet(v string, set []string) bool {
	for _, x := range set {
		if x == v {
			return true
		}
	}
	return false
}

var (
	titleWords = normSet("title", "titles", "titre", "titres", "titulo", "titulos", "baslik", "baslık", "başlık", "basligi", "baslıgı", "başlığı",
		"adi", "adı", "nombre", "nome", "nom", "name", "names", "titel", "titolo", "titulo original", "tytul", "название", "наименование",
		"название видео", "動画タイトル", "動画のタイトル", "제목", "视频标题", "影片標題", "naam", "عنوان", "शीर्षक", "τίτλος")
	playlistWords = normSet("playlist", "playlists", "oynatma listesi", "oynatma listeleri", "playlistler", "lista de reproduccion",
		"listas de reproduccion", "liste de lecture", "listes de lecture", "lista di riproduzione", "liste di riproduzione", "lista de reproducao",
		"listas de reproducao", "wiedergabeliste", "wiedergabelisten", "afspeellijst", "afspeellijsten", "список воспроизведения",
		"списки воспроизведения", "плейлист", "плейлисты", "再生リスト", "재생목록", "播放列表", "播放清單", "قائمة التشغيل", "قوائم التشغيل",
		"प्लेलिस्ट", "प्लेलिस्टें", "λίστα αναπαραγωγής", "λίστες αναπαραγωγής", "danh sach phat", "รายการเล่น", "เพลย์ลิสต์", "רשימת השמעה",
		"רשימות השמעה")
	channelWords = normSet("channel", "chaine", "canal", "canale", "kanal", "kanaal", "канал", "канали", "kanał", "チャンネル", "채널", "频道",
		"頻道", "قناة", "चैनल", "κανάλι", "kenh", "ช่อง", "ערוץ")
	videoWords = normSet("video", "videos", "vidéo", "vidéos", "vídeo", "vídeos", "動画", "동영상", "비디오", "видео", "视频", "影片", "film",
		"filmy", "فيديو", "فيديوهات", "वीडियो", "βίντεο", "วิดีโอ", "ভিডিও", "סרטון")
	subscriptionWords = normSet("subscriptions", "suscripciones", "inscricoes", "inscrições", "iscrizioni", "abbonamenti", "abos", "abonelikler",
		"abonnements", "abonnementen", "abonnierte kanäle", "subskrypcje", "подписки", "підписки", "登録チャンネル", "チャンネル登録", "구독", "订阅",
		"訂閱", "الاشتراكات", "การสมัครรับข้อมูล", "การติดตาม", "מינויים", "সাবস্ক্রিপশন", "kenh da dang ky")
	playlistManifestNames = normSet("playlists", "oynatma listeleri", "oynatma listesi", "listas de reproduccion", "listes de lecture",
		"liste di riproduzione", "listas de reproducao", "wiedergabelisten", "wiedergabeliste", "afspeellijsten", "afspeellijst",
		"списки воспроизведения", "список воспроизведения", "再生リスト", "재생목록", "播放列表", "播放清單", "قوائم التشغيل", "قائمة التشغيل",
		"danh sach phat", "รายการเล่น", "เพลย์ลิสต์", "רשימת השמעה", "רשימות השמעה")
	temporalWords = normSet("added", "date", "time", "timestamp", "creation", "created", "fecha", "marca de tiempo", "creacion", "ajoute",
		"datum", "zeit", "erstellt", "data", "ora", "creazione", "criacao", "eklenme", "olusturma", "дата", "время", "создания", "добавления",
		"日時", "时间", "日期", "创建", "추가", "생성", "التاريخ", "وقت", "إضافة", "बनाने", "समय", "ημερομηνία", "เวลา")
	identifierWords = []string{"id", "identifier", "identificador", "identifiant", "kimligi", "kimliği", "identyfikator", "identifikator", "kennung"}
	identifierParts = []string{"идентификатор", "識別子", "標識符", "标识符", "معرف"}

	channelIDRe   = regexp.MustCompile(`^(UC|HC)[A-Za-z0-9_-]{10,}$`)
	videoIDRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{6,}$`)
	playlistIDRe  = regexp.MustCompile(`^(PL|UU|LL|RD|OLAK5uy_)[A-Za-z0-9_-]{6,}$`)
	channelLinkRe = regexp.MustCompile(`(?i)youtube\.com/(channel/|@|c/|user/)`)
)

func words(n string) []string { return strings.Fields(n) }

func hasIdentifier(n string) bool {
	parts := words(n)
	for _, w := range identifierWords {
		if inSet(w, parts) {
			return true
		}
	}
	for _, w := range identifierParts {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func isChannelIDHeader(v string) bool {
	n := takeoutNorm(v)
	return n == "channel id" || n == "id des chaines" || n == "id de la chaine" || containsAnyNorm(n, channelWords) && hasIdentifier(n)
}

func isChannelURLHeader(v string) bool {
	n := takeoutNorm(v)
	return n == "channel url" || n == "url des chaines" || n == "url de la chaine" || inSet("url", words(n)) && containsAnyNorm(n, channelWords)
}

func isChannelTitleHeader(v string) bool {
	n := takeoutNorm(v)
	return n == "titres des chaines" || n == "titre de la chaine" ||
		containsAnyNorm(n, titleWords) && (containsAnyNorm(n, channelWords) || inSet(n, titleWords))
}

func isPlaylistText(v string) bool { return containsAnyNorm(takeoutNorm(v), playlistWords) }

func isPlaylistIDHeader(v string) bool {
	n := takeoutNorm(v)
	return n == "playlist id" || n == "id de la playlist" || isPlaylistText(n) && hasIdentifier(n)
}

func isPlaylistTitleHeader(v string) bool {
	n := takeoutNorm(v)
	return isPlaylistText(n) && containsAnyNorm(n, titleWords)
}

func isVideoIDHeader(v string) bool {
	n := takeoutNorm(v)
	return n == "video id" || n == "id video" || containsAnyNorm(n, videoWords) && hasIdentifier(n)
}

func isVideoTitleHeader(v string) bool {
	n := takeoutNorm(v)
	return containsAnyNorm(n, videoWords) && containsAnyNorm(n, titleWords)
}

func isAddedAtHeader(v string) bool {
	n := takeoutNorm(v)
	return containsAnyNorm(n, temporalWords) && (isPlaylistText(n) || containsAnyNorm(n, videoWords) || strings.Contains(n, "added at") ||
		strings.Contains(n, "date added") || strings.Contains(n, "created at") || n == "timestamp")
}

func isURLHeader(v string) bool { return inSet("url", words(takeoutNorm(v))) }

func looksLikeChannelID(v string) bool  { return channelIDRe.MatchString(strings.TrimSpace(v)) }
func looksLikePlaylistID(v string) bool { return playlistIDRe.MatchString(strings.TrimSpace(v)) }

func looksLikeVideoID(v string) bool {
	t := strings.TrimSpace(v)
	return videoIDRe.MatchString(t) && !looksLikeChannelID(t) && !looksLikePlaylistID(t)
}

func looksLikeLikelyVideoID(v string) bool {
	return len([]rune(strings.TrimSpace(v))) >= 10 && looksLikeVideoID(v)
}

func containsChannelURL(v string) bool { return channelLinkRe.MatchString(v) }

func containsWatchURL(v string) bool {
	l := strings.ToLower(v)
	return strings.Contains(l, "youtube.com/watch?v=") || strings.Contains(l, "youtube.com/shorts/") || strings.Contains(l, "youtube.com/live/") ||
		strings.Contains(l, "youtu.be/")
}

func isPlaylistManifestName(v string) bool { return inSet(takeoutNorm(v), playlistManifestNames) }
func isSubscriptionText(v string) bool     { return containsAnyNorm(takeoutNorm(v), subscriptionWords) }

const (
	watchLaterKey = "Watch later"
	likedKey      = "Liked videos"
)

var (
	watchLaterPhrases = normSet("watch later", "a regarder plus tard", "regarder plus tard", "ver mas tarde", "assistir mais tarde",
		"ver mais tarde", "guarda piu tardi", "daha sonra izle", "daha sonra izlenecekler", "spater ansehen", "später ansehen", "later bekijken",
		"obejrzyj pozniej", "посмотреть позже", "смотреть позже", "後で見る", "나중에 볼 동영상", "稍后观看", "稍後觀看", "بعدا تماشا کنید",
		"مشاهدة لاحقا", "المشاهدة لاحقا", "переглянути пізніше", "बाद में देखें", "ดูภายหลัง", "παρακολούθηση αργότερα")
	likedPlaylistPhrases = normSet("liked videos", "videos liked", "videos que j aime", "j aime", "videos que me gustan", "videos que gostei",
		"videos curtidos", "videos de que gostei", "videos que eu gostei", "video piaciuti", "video che mi piacciono", "begendigim videolar",
		"begendiğim videolar", "beğendigim videolar", "beğendiğim videolar", "begenilen videolar", "beğenilen videolar", "mit gefallt",
		"gefällt mir", "videos die mir gefallen", "leuke videos", "polubione filmy", "понравившиеся видео", "понравились видео",
		"高く評価した動画", "좋아요 표시한 동영상", "喜欢的视频", "喜歡的影片", "الفيديوهات التي أعجبتني", "वीडियो जो मुझे पसंद हैं",
		"βίντεο που μου αρέσουν", "улюблені відео")
	historyMarkers = normSet("watch history", "historique", "historique des videos regardees", "historico", "historico de visualizacao",
		"historico de exibicao", "historial", "historial de reproduccion", "historial de visualizacion", "cronologia", "cronologia de reproduccion",
		"verlauf", "wiedergabeverlauf", "kijkgeschiedenis", "historia ogladania", "izleme gecmisi", "lich su xem", "ประวัติการดู",
		"ประวัติการรับชม", "история просмотров", "история просмотра", "історія перегляду", "視聴履歴", "再生履歴", "시청 기록", "观看记录",
		"觀看記錄", "سجل المشاهدة", "تاریخچه تماشا", "देखने का इतिहास")
	unavailableTitles = normSet("deleted video", "private video", "video deleted", "video indisponible", "video no disponible", "video privee",
		"video privado", "video supprimee", "video unavailable")
)

// systemPlaylist is YoutubeTakeoutSystemPlaylist.canonicalKey: Watch later, Liked videos, or "".
func systemPlaylist(v string) string {
	n := takeoutNorm(v)
	switch {
	case containsAnyNorm(n, watchLaterPhrases):
		return watchLaterKey
	case containsAnyNorm(n, likedPlaylistPhrases):
		return likedKey
	}
	return ""
}

func isYoutubeHTML(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ".html") && strings.Contains(strings.ToLower(p), "youtube")
}

func isHistoryPath(p string) bool { return containsAnyNorm(takeoutNorm(p), historyMarkers) }

func isUnavailableTitle(t string) bool { return inSet(takeoutNorm(t), unavailableTitles) }

// Ads: Takeout lists the ads a user was shown among watched videos and marks them only in the entry's details.
const htmlCellStart = `<div class="outer-cell`

var (
	adWords   = []string{"ads", "advertisement", "advertisements", "annonces", "werbung"}
	adPhrases = []string{"google ads", "annonces google"}
	tagRe     = regexp.MustCompile(`<[^>]+>`)
)

func isAdDetail(v string) bool {
	n := takeoutNorm(v)
	return inSet(n, adWords) || containsAnyNorm(n, adPhrases)
}

// isAdEntry is YoutubeTakeoutAdMarker.isAdEntry: a known ad detail, or the shape of an ad (no channel, at least
// three labelled blocks in the caption).
func isAdEntry(html string, from, until int, hasChannel bool) bool {
	if until <= from {
		return false
	}
	entry := html[from:until]
	if i := strings.Index(entry, htmlCellStart); i >= 0 {
		entry = entry[:i]
	}
	for _, part := range splitAll(entry, "<br>", "&emsp;") {
		if isAdDetail(tagRe.ReplaceAllString(part, " ")) {
			return true
		}
	}
	if hasChannel {
		return false
	}
	i := strings.Index(entry, "mdl-typography--caption")
	if i < 0 {
		return false
	}
	return strings.Count(strings.ToLower(entry[i+len("mdl-typography--caption"):]), "<b>") >= 3
}

func splitAll(s string, seps ...string) []string {
	parts := []string{s}
	for _, sep := range seps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	return parts
}

// Activity verbs, YoutubeTakeoutActivityClassifier.
var (
	watchedPhrases = normSet("You watched", "Viewed", "Vous avez regardé", "Has visto", "Has visto el vídeo", "Ha visto", "Você assistiu",
		"Você assistiu a", "Você assistiu ao vídeo", "hai guardato", "Hai guardato", "Hai guardato il video", "İzlediniz", "du hast angesehen",
		"Du hast dir angesehen", "Du hast dir das angesehen", "je hebt gekeken", "je hebt gekeken naar", "obejrzałeś", "obejrzałaś", "obejrzano",
		"вы смотрели", "вы посмотрели", "ви дивилися", "ви переглянули", "du tittade på", "du har sett", "du så på", "katsoit", "sledovali jste",
		"ai vizionat", "anda menonton", "bạn đã xem", "आपने देखा", "あなたが視聴した動画", "視聴した動画", "視聴しました", "動画を視聴しました",
		"시청한 동영상", "동영상을 시청했습니다", "已观看", "已觀看", "观看了", "觀看了", "شاهدت", "لقد شاهدت")
	watchedSuffixes = normSet("izlənildi", "を視聴しました", "を再生しました")
	likedPhrases    = normSet("You liked", "Liked", "Vous avez aimé", "A aimé", "te ha gustado", "te gustó", "ha gustado", "Você gostou",
		"Você curtiu", "Você marcou como gostei", "hai messo mi piace", "ti è piaciuto", "Beğendiniz", "gefällt mir", "dir gefällt",
		"je hebt dit leuk gevonden", "podobał ci się", "понравилось", "вы поставили отметку нравится", "сподобалося", "du gillade",
		"du kunne godt lide", "du likte", "pidit", "líbilo se vám", "ți-a plăcut", "anda menyukai", "bạn đã thích", "आपको पसंद आया",
		"高く評価しました", "좋아요 표시함", "点赞了", "按讚", "您喜欢了", "أعجبك")
	subscribedPhrases = normSet("You subscribed to", "Vous vous êtes abonné à", "te has suscrito a", "Você se inscreveu em",
		"Você se inscreveu no canal", "ti sei iscritto a", "ti sei iscritta a", "Abone oldunuz", "du hast abonniert", "du hast den kanal abonniert",
		"je hebt je geabonneerd op", "zasubskrybowałeś", "zasubskrybowałaś", "вы подписались на", "вы подписались на канал", "ви підписалися на",
		"du prenumererade på", "du abonnerede på", "du abonnerte på", "tilasit", "te-ai abonat la", "anda berlangganan", "bạn đã đăng ký",
		"आपने सदस्यता ली", "登録しました", "チャンネル登録しました", "구독함", "구독했습니다", "已订阅", "已訂閱", "订阅了", "訂閱了", "اشتركت في",
		"لقد اشتركت في")
)

func startsWithAnyNorm(v string, phrases []string) bool {
	n := takeoutNorm(v)
	for _, p := range phrases {
		if n == p || strings.HasPrefix(n, p+" ") {
			return true
		}
	}
	return false
}

func isWatched(v string) bool          { return containsAnyNorm(takeoutNorm(v), watchedPhrases) }
func isLiked(v string) bool            { return containsAnyNorm(takeoutNorm(v), likedPhrases) }
func isSubscribed(v string) bool       { return containsAnyNorm(takeoutNorm(v), subscribedPhrases) }
func isLikedAction(v string) bool      { return startsWithAnyNorm(v, likedPhrases) }
func isSubscribedAction(v string) bool { return startsWithAnyNorm(v, subscribedPhrases) }

func isWatchedAction(v string) bool {
	if startsWithAnyNorm(v, watchedPhrases) {
		return true
	}
	n := takeoutNorm(v)
	for _, s := range watchedSuffixes {
		if strings.HasSuffix(n, " "+s) {
			return true
		}
	}
	return false
}

func isLikedSuffix(v string) bool {
	n := takeoutNorm(v)
	for _, p := range likedPhrases {
		if n == p || strings.HasPrefix(n, p) || strings.HasSuffix(n, p) {
			return true
		}
	}
	return false
}
