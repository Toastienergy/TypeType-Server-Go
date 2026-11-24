package portability

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// takeoutDate is YoutubeTakeoutDateParser: ISO instants and dates, and the localized activity dates Takeout writes
// in its HTML ("Sep 16, 2026, 6:02:08 PM CEST", "16 sept. 2026, 18:02:08 CEST", "2026年9月4日 午後9:45:27 JST").
// Java tries forty patterns in every JDK locale; this reads the parts directly: a time, an optional day period, a
// date (numeric, CJK or with a month name) and a zone abbreviation resolved to its region, daylight saving
// included, as Java resolves zone names.
func takeoutDate(v string) (int64, bool) {
	s := strings.TrimSpace(spacesRe.ReplaceAllString(v, " "))
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), true
		}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UnixMilli(), true
	}
	return activityDate(s)
}

var (
	spacesRe    = regexp.MustCompile(`[\p{Z}\s]+`)
	connectorRe = regexp.MustCompile(`(?i)\s+(?:de|del)\s+`)
	clockRe     = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})`)
	periodRe    = regexp.MustCompile(`凌晨|清晨|早晨|早上|上午|中午|下午|晚上|半夜|午前|午後|오전|오후|صباحًا|صباحا|صباح|مساءً|مساءا|مساء|قبل الظهر|بعد الظهر|पूर्वाह्न|अपराह्न|सुबह|दोपहर|शाम|रात`)
	cjkDateRe   = regexp.MustCompile(`(\d{4})\s*[年년]\s*(\d{1,2})\s*[月월]\s*(\d{1,2})\s*[日일]?`)
	dayMonthRe  = regexp.MustCompile(`^(\d{1,2})\s*(\d{1,2})\s*[月월]\s*(\d{4})$`)
	numericRe   = regexp.MustCompile(`^(\d{1,4})[/.\-](\d{1,2})[/.\-](\d{1,4})$`)
	letterRe    = regexp.MustCompile(`[\p{L}\p{M}][\p{L}\p{M}.]*`)
	digitsRe    = regexp.MustCompile(`\d+`)
	offsetZone  = regexp.MustCompile(`^(?i)(?:GMT|UTC)(?:([+-])(\d{1,2})(?::?(\d{2}))?)?$`)
)

var periods = map[string]string{"凌晨": "AM", "清晨": "AM", "早晨": "AM", "早上": "AM", "上午": "AM", "中午": "PM", "下午": "PM", "晚上": "PM",
	"半夜": "AM", "午前": "AM", "午後": "PM", "오전": "AM", "오후": "PM", "صباحًا": "AM", "صباحا": "AM", "صباح": "AM", "مساءً": "PM",
	"مساءا": "PM", "مساء": "PM", "قبل الظهر": "AM", "بعد الظهر": "PM", "पूर्वाह्न": "AM", "अपराह्न": "PM", "सुबह": "AM", "दोपहर": "PM",
	"शाम": "PM", "रात": "PM"}

// zoneRegions maps the abbreviations Takeout prints to the region Java's zone-name parser picks for them. A
// China-formatted date reading CST means China Standard Time, which Java resolves to Chicago.
var zoneRegions = map[string]string{
	"UTC": "UTC", "GMT": "UTC", "Z": "UTC", "UT": "UTC",
	"CET": "Europe/Paris", "CEST": "Europe/Paris", "MEZ": "Europe/Berlin", "MESZ": "Europe/Berlin",
	"EET": "Europe/Athens", "EEST": "Europe/Athens", "WET": "Europe/Lisbon", "WEST": "Europe/Lisbon",
	"BST": "Europe/London", "IST": "Asia/Kolkata", "MSK": "Europe/Moscow", "TRT": "Europe/Istanbul",
	"EST": "America/New_York", "EDT": "America/New_York", "CST": "America/Chicago", "CDT": "America/Chicago",
	"MST": "America/Denver", "MDT": "America/Denver", "PST": "America/Los_Angeles", "PDT": "America/Los_Angeles",
	"AKST": "America/Anchorage", "AKDT": "America/Anchorage", "HST": "Pacific/Honolulu", "AST": "America/Halifax",
	"ADT": "America/Halifax", "NST": "America/St_Johns", "NDT": "America/St_Johns", "BRT": "America/Sao_Paulo",
	"ART": "America/Argentina/Buenos_Aires", "JST": "Asia/Tokyo", "KST": "Asia/Seoul", "HKT": "Asia/Hong_Kong",
	"SGT": "Asia/Singapore", "PHT": "Asia/Manila", "PHST": "Asia/Manila", "ICT": "Asia/Bangkok", "WIB": "Asia/Jakarta",
	"PKT": "Asia/Karachi", "AEST": "Australia/Sydney", "AEDT": "Australia/Sydney", "ACST": "Australia/Adelaide",
	"ACDT": "Australia/Adelaide", "AWST": "Australia/Perth", "NZST": "Pacific/Auckland", "NZDT": "Pacific/Auckland",
	"SAST": "Africa/Johannesburg", "EAT": "Africa/Nairobi", "WAT": "Africa/Lagos", "CAT": "Africa/Maputo",
}

func zoneFor(token string, cjk bool) (*time.Location, bool) {
	t := strings.ToUpper(strings.TrimSpace(token))
	if m := offsetZone.FindStringSubmatch(t); m != nil {
		if m[1] == "" {
			return time.UTC, true
		}
		h, _ := strconv.Atoi(m[2])
		mins, _ := strconv.Atoi(m[3])
		off := h*3600 + mins*60
		if m[1] == "-" {
			off = -off
		}
		return time.FixedZone(t, off), true
	}
	if cjk && t == "CST" {
		return time.FixedZone("CST", 8*3600), true
	}
	name, ok := zoneRegions[t]
	if !ok {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	return loc, err == nil
}

func activityDate(s string) (int64, bool) {
	s = strings.NewReplacer("،", ",", "，", ",").Replace(s)
	pm, am := false, false
	s = periodRe.ReplaceAllStringFunc(s, func(p string) string {
		if periods[p] == "PM" {
			pm = true
		} else {
			am = true
		}
		return " "
	})
	s = strings.TrimSpace(spacesRe.ReplaceAllString(connectorRe.ReplaceAllString(s, " "), " "))
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, false
	}
	zoneToken := fields[len(fields)-1]
	rest := strings.Join(fields[:len(fields)-1], " ")
	clock := clockRe.FindStringSubmatchIndex(rest)
	if clock == nil {
		return 0, false
	}
	hour, _ := strconv.Atoi(rest[clock[2]:clock[3]])
	minute, _ := strconv.Atoi(rest[clock[4]:clock[5]])
	second, _ := strconv.Atoi(rest[clock[6]:clock[7]])
	around := strings.ToUpper(rest[:clock[0]] + " " + rest[clock[1]:])
	for _, w := range strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(around)) {
		switch w {
		case "PM", "P M":
			pm = true
		case "AM", "A M":
			am = true
		}
	}
	if pm || am {
		if hour < 1 || hour > 12 {
			return 0, false
		}
		hour %= 12
		if pm {
			hour += 12
		}
	}
	datePart := strings.TrimSpace(rest[:clock[0]])
	datePart = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(regexp.MustCompile(`(?i)\b[AP]\.?\s?M\.?$`).ReplaceAllString(datePart, "")), ","))
	cjk := cjkDateRe.MatchString(datePart) || strings.ContainsAny(datePart, "月월年년")
	y, mo, d, ok := parseDatePart(datePart)
	if !ok || hour > 23 || minute > 59 || second > 59 {
		return 0, false
	}
	loc, ok := zoneFor(zoneToken, cjk)
	if !ok {
		return 0, false
	}
	t := time.Date(y, time.Month(mo), d, hour, minute, second, 0, loc)
	if t.Day() != d || int(t.Month()) != mo {
		return 0, false
	}
	return t.UnixMilli(), true
}

func parseDatePart(p string) (y, m, d int, ok bool) {
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if c := cjkDateRe.FindStringSubmatch(p); c != nil {
		return atoi(c[1]), atoi(c[2]), atoi(c[3]), true
	}
	compact := strings.TrimSpace(strings.Trim(p, ","))
	if c := dayMonthRe.FindStringSubmatch(compact); c != nil {
		return atoi(c[3]), atoi(c[2]), atoi(c[1]), true
	}
	if c := numericRe.FindStringSubmatch(compact); c != nil {
		a, b, e := atoi(c[1]), atoi(c[2]), atoi(c[3])
		switch {
		case len(c[1]) == 4:
			return a, b, e, b >= 1 && b <= 12
		case len(c[3]) == 4 && b <= 12 && a <= 31:
			return e, b, a, b >= 1
		case len(c[3]) == 4 && a <= 12:
			return e, a, b, a >= 1
		}
		return 0, 0, 0, false
	}
	month := 0
	for _, tok := range letterRe.FindAllString(p, -1) {
		if n, ok := monthAlias[strings.ReplaceAll(takeoutNorm(tok), " ", "")]; ok {
			month = n
			break
		}
	}
	nums := digitsRe.FindAllString(p, -1)
	if month == 0 || len(nums) != 2 {
		return 0, 0, 0, false
	}
	a, b := atoi(nums[0]), atoi(nums[1])
	switch {
	case len(nums[1]) == 4:
		return b, month, a, a >= 1 && a <= 31
	case len(nums[0]) == 4:
		return a, month, b, b >= 1 && b <= 31
	}
	return 0, 0, 0, false
}

// monthNames holds, per language, each month's spellings in the forms Java's FULL, SHORT and standalone styles
// give; an alias shared by two months is dropped, as YoutubeTakeoutDateParser drops it.
var monthNames = [][12]string{
	{"january|jan", "february|feb", "march|mar", "april|apr", "may", "june|jun", "july|jul", "august|aug", "september|sep|sept", "october|oct", "november|nov", "december|dec"},
	{"janvier|janv", "fevrier|fevr", "mars", "avril|avr", "mai", "juin", "juillet|juil", "aout", "septembre|sept", "octobre|oct", "novembre|nov", "decembre|dec"},
	{"enero|ene", "febrero|feb", "marzo|mar", "abril|abr", "mayo|may", "junio|jun", "julio|jul", "agosto|ago", "septiembre|setiembre|sept|sep|set", "octubre|oct", "noviembre|nov", "diciembre|dic"},
	{"januar|jan|janner|jänner|jän", "februar|feb", "marz|märz|mar|mär", "april|apr", "mai", "juni|jun", "juli|jul", "august|aug", "september|sep|sept", "oktober|okt", "november|nov", "dezember|dez"},
	{"gennaio|gen", "febbraio|feb", "marzo|mar", "aprile|apr", "maggio|mag", "giugno|giu", "luglio|lug", "agosto|ago", "settembre|set", "ottobre|ott", "novembre|nov", "dicembre|dic"},
	{"janeiro|jan", "fevereiro|fev", "marco|março|mar", "abril|abr", "maio|mai", "junho|jun", "julho|jul", "agosto|ago", "setembro|set", "outubro|out", "novembro|nov", "dezembro|dez"},
	{"ocak|oca", "subat|şubat|sub|şub", "mart|mar", "nisan|nis", "mayis|mayıs|may", "haziran|haz", "temmuz|tem", "agustos|ağustos|agu|ağu", "eylul|eylül|eyl", "ekim|eki", "kasim|kasım|kas", "aralik|aralık|ara"},
	{"januari|jan", "februari|feb", "maart|mrt", "april|apr", "mei", "juni|jun", "juli|jul", "augustus|aug", "september|sep|sept", "oktober|okt", "november|nov", "december|dec"},
	{"styczen|stycznia|sty", "luty|lutego|lut", "marzec|marca|mar", "kwiecien|kwietnia|kwi", "maj|maja", "czerwiec|czerwca|cze", "lipiec|lipca|lip", "sierpien|sierpnia|sie", "wrzesien|wrzesnia|wrz", "pazdziernik|pazdziernika|paz", "listopad|listopada|lis", "grudzien|grudnia|gru"},
	{"январь|января|янв", "февраль|февраля|февр|фев", "март|марта|мар", "апрель|апреля|апр", "май|мая", "июнь|июня|июн", "июль|июля|июл", "август|августа|авг", "сентябрь|сентября|сент|сен", "октябрь|октября|окт", "ноябрь|ноября|нояб|ноя", "декабрь|декабря|дек"},
	{"січень|січня|січ", "лютий|лютого|лют", "березень|березня|бер", "квітень|квітня|квіт|кві", "травень|травня|трав|тра", "червень|червня|черв|чер", "липень|липня|лип", "серпень|серпня|серп|сер", "вересень|вересня|вер", "жовтень|жовтня|жовт|жов", "листопад|листопада|лист|лис", "грудень|грудня|груд|гру"},
	{"يناير|كانون الثاني", "فبراير|شباط", "مارس|آذار", "أبريل|ابريل|نيسان", "مايو|أيار", "يونيو|يونيه|حزيران", "يوليو|يوليه|تموز", "أغسطس|اغسطس|آب", "سبتمبر|أيلول", "أكتوبر|اكتوبر|تشرين الأول", "نوفمبر|تشرين الثاني", "ديسمبر|كانون الأول"},
	{"जनवरी|जन", "फ़रवरी|फरवरी|फ़र", "मार्च|मा", "अप्रैल|अप्रै", "मई", "जून", "जुलाई|जुला", "अगस्त|अग", "सितंबर|सितम्बर|सित", "अक्टूबर|अक्तूबर|अक्टू", "नवंबर|नवम्बर|नव", "दिसंबर|दिसम्बर|दिस"},
	{"ιανουαριος|ιανουαριου|ιαν", "φεβρουαριος|φεβρουαριου|φεβ", "μαρτιος|μαρτιου|μαρ", "απριλιος|απριλιου|απρ", "μαιος|μαιου|μαι", "ιουνιος|ιουνιου|ιουν", "ιουλιος|ιουλιου|ιουλ", "αυγουστος|αυγουστου|αυγ", "σεπτεμβριος|σεπτεμβριου|σεπ", "οκτωβριος|οκτωβριου|οκτ", "νοεμβριος|νοεμβριου|νοε", "δεκεμβριος|δεκεμβριου|δεκ"},
	{"januari|jan", "februari|feb", "maret|mar", "april|apr", "mei", "juni|jun", "juli|jul", "agustus|agu|ags", "september|sep", "oktober|okt", "november|nov", "desember|des"},
	{"januari|jan", "februari|feb", "mars|mar", "april|apr", "maj", "juni|jun", "juli|jul", "augusti|aug", "september|sep|sept", "oktober|okt", "november|nov", "december|dec"},
	{"januar|jan", "februar|feb", "marts|mar", "april|apr", "maj", "juni|jun", "juli|jul", "august|aug", "september|sep", "oktober|okt", "november|nov", "december|dec"},
	{"januar|jan", "februar|feb", "mars|mar", "april|apr", "mai", "juni|jun", "juli|jul", "august|aug", "september|sep", "oktober|okt", "november|nov", "desember|des"},
	{"tammikuu|tammikuuta|tammik", "helmikuu|helmikuuta|helmik", "maaliskuu|maaliskuuta|maalisk", "huhtikuu|huhtikuuta|huhtik", "toukokuu|toukokuuta|toukok", "kesakuu|kesakuuta|kesak", "heinakuu|heinakuuta|heinak", "elokuu|elokuuta|elok", "syyskuu|syyskuuta|syysk", "lokakuu|lokakuuta|lokak", "marraskuu|marraskuuta|marrask", "joulukuu|joulukuuta|jouluk"},
	{"leden|ledna|led", "unor|unora|uno", "brezen|brezna|bre", "duben|dubna|dub", "kveten|kvetna|kve", "cerven|cervna|cvn", "cervenec|cervence|cvc", "srpen|srpna|srp", "zari|zar", "rijen|rijna|rij", "listopad|listopadu|lis", "prosinec|prosince|pro"},
	{"ianuarie|ian", "februarie|feb", "martie|mar", "aprilie|apr", "mai", "iunie|iun", "iulie|iul", "august|aug", "septembrie|sept|sep", "octombrie|oct", "noiembrie|nov|noi", "decembrie|dec"},
	{"januar|jan", "februar|feb", "marec|mar", "april|apr", "maj", "junij|jun", "julij|jul", "avgust|avg", "september|sep", "oktober|okt", "november|nov", "december|dec"},
	{"januar|januara|jan", "februar|februara|feb", "mart|marta|mar", "april|aprila|apr", "maj|maja", "jun|juna", "jul|jula", "avgust|avgusta|avg", "septembar|septembra|sep", "oktobar|oktobra|okt", "novembar|novembra|nov", "decembar|decembra|dec"},
	{"sijecanj|sijecnja|sij", "veljaca|veljace|velj", "ozujak|ozujka|ozu", "travanj|travnja|tra", "svibanj|svibnja|svi", "lipanj|lipnja|lip", "srpanj|srpnja|srp", "kolovoz|kolovoza|kol", "rujan|rujna|ruj", "listopad|listopada|lis", "studeni|studenoga|stu", "prosinac|prosinca|pro"},
	{"januar|jan", "februar|feb", "marec|mar", "april|apr", "maj", "jun", "jul", "august|aug", "september|sep", "oktober|okt", "november|nov", "december|dec"},
	{"januar|jan", "februar|feb", "marz|mar", "april|apr", "mai", "juni|jun", "juli|jul", "august|aug", "september|sep", "oktober|okt", "november|nov", "dezember|dez"},
	{"januar|jan", "februar|feb", "marec|mar", "april|apr", "maj", "jun", "jul", "avgust|avg", "september|sep", "oktober|okt", "november|nov", "december|dec"},
	{"januar|jan", "februar|feb", "mart|mar", "april|apr", "maj", "jun", "jul", "avgust|avg", "septembar|sep", "oktobar|okt", "novembar|nov", "decembar|dec"},
	{"január|jan", "február|febr|feb", "március|marc|mar", "április|apr", "május|maj", "június|jun", "július|jul", "augusztus|aug", "szeptember|szept|sze", "október|okt", "november|nov", "december|dec"},
	{"ינואר", "פברואר", "מרץ", "אפריל", "מאי", "יוני", "יולי", "אוגוסט", "ספטמבר", "אוקטובר", "נובמבר", "דצמבר"},
	{"มกราคม|มค", "กุมภาพันธ์|กพ", "มีนาคม|มีค", "เมษายน|เมย", "พฤษภาคม|พค", "มิถุนายน|มิย", "กรกฎาคม|กค", "สิงหาคม|สค", "กันยายน|กย", "ตุลาคม|ตค", "พฤศจิกายน|พย", "ธันวาคม|ธค"},
}

var monthAlias = func() map[string]int {
	seen := map[string]map[int]bool{}
	for _, lang := range monthNames {
		for i, names := range lang {
			for _, n := range strings.Split(names, "|") {
				a := strings.ReplaceAll(takeoutNorm(n), " ", "")
				if len([]rune(a)) < 2 {
					continue
				}
				if seen[a] == nil {
					seen[a] = map[int]bool{}
				}
				seen[a][i+1] = true
			}
		}
	}
	out := map[string]int{}
	for a, months := range seen {
		if len(months) == 1 {
			for m := range months {
				out[a] = m
			}
		}
	}
	return out
}()
