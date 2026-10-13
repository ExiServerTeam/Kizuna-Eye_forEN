// insight パッケージはアラート履歴を、直近のセキュリティ状況の小さく安定した
// 絵へ凝縮する。
//
//   - Threat:  期間リスク指数と、それを押し上げたカテゴリ。
//   - Profile: 誰がアラートを起こしたか（送信元アドレス）、どんな手法が使われ、いつか。
//   - Posture: 設定チェックリストのスコア。
//
// 設計方針 (内部設計書 2.3 / 第4章):
//   - コアは「観測と可視化」に徹する。ここは解釈を足すが、判断や対処はしない
//     （ブロックも通知もしない）。集計結果を返すだけ。
//   - 純粋関数のみ。時刻と入力は呼び出し側が与える（テスト可能性）。
//   - 新しい永続データを作らない。入力は alert.HistoryEntry だけ。
//   - 攻撃者が制御できる文字列（メッセージ・タイトル）は、集計キーとして
//     使う前に必ず正規化する。UI に返すのは検証済みの IP と正規化済みの
//     カテゴリだけで、生のメッセージは返さない。
//   - pkg/module / pkg/status / pkg/fsutil を変更しない（共有パッケージを
//     触ると agent と全 .so の同時再ビルドが必要になるため）。
// Package insight condenses the alert history into a small, stable picture of
// the recent security situation:
//
//   - Threat:  a period risk index plus the categories that drove it.
//   - Profile: who triggered alerts (source addresses), what techniques were
//     used, and when.
//   - Posture: a configuration checklist score.
//
// 設計方針 (内部設計書 2.3 / 第4章):
//   - コアは「観測と可視化」に徹する。ここは解釈を足すが、判断や対処はしない
//     （ブロックも通知もしない）。集計結果を返すだけ。
//   - 純粋関数のみ。時刻と入力は呼び出し側が与える（テスト可能性）。
//   - 新しい永続データを作らない。入力は alert.HistoryEntry だけ。
//   - 攻撃者が制御できる文字列（メッセージ・タイトル）は、集計キーとして
//     使う前に必ず正規化する。UI に返すのは検証済みの IP と正規化済みの
//     カテゴリだけで、生のメッセージは返さない。
//   - pkg/module / pkg/status / pkg/fsutil を変更しない（共有パッケージを
//     触ると agent と全 .so の同時再ビルドが必要になるため）。
package insight

import (
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"Kizuna-Eye/pkg/alert"
)

// DefaultPeriod は呼び出し側が指定しない場合に使う窓だ。
// DefaultPeriod is the window used when the caller does not specify one.
const DefaultPeriod = 7 * 24 * time.Hour

// maxList は API が返すすべてのリストを制限し、アラートの洪水が応答を
// 無制限にしないようにする。
// maxList caps every list the API returns, so a flood of alerts cannot make
// the response unbounded.
const maxList = 10

// ---------------------------------------------------------------------------
// 共通ヘルパー
// ---------------------------------------------------------------------------

// levelWeight は 1 件のアラートのリスク寄与だ。復旧/成功アラートは良い知らせで、
// 指数には何も足さない。
// levelWeight is the risk contribution of one alert. Recovery/success alerts
// are good news and add nothing to the index.
func levelWeight(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical":
		return 8
	case "warning":
		return 3
	case "success":
		return 0
	case "info":
		return 1
	}
	// 未知のレベルは無視せず info と同様に扱う。レベルが綴り間違いのアラートが
	// カウントから消えてはならない。
	// An unknown level is treated like info rather than ignored: an alert
	// whose level is misspelled must not disappear from the count.
	return 1
}

var categoryRe = regexp.MustCompile(`[^a-z0-9_]+`)

// CategoryOf はアラート型を安定した集計キーへ正規化する。
// "security_ssh_failed" -> "ssh_failed"。異常なものは [a-z0-9_] へ畳み込まれ、
// 上限付きになる。これにより攻撃者が制御する型がマークアップを注入したり、
// キー空間を爆発させたりできない。
// CategoryOf normalizes an alert type into a stable aggregation key.
// "security_ssh_failed" -> "ssh_failed". Anything unusual collapses to
// [a-z0-9_] and is capped, so an attacker-controlled type cannot inject markup
// or blow up the key space.
func CategoryOf(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	t = strings.TrimPrefix(t, "security_")
	t = categoryRe.ReplaceAllString(t, "_")
	t = strings.Trim(t, "_")
	if t == "" {
		return "other"
	}
	if len(t) > 48 {
		t = t[:48]
	}
	return t
}

// ---------------------------------------------------------------------------
// IP 抽出
// ---------------------------------------------------------------------------

// isIPByte は c が IP 候補に現れ得るかを返す。16 進数字、':' または '.' だ。
// IPv6 は 16 進 + ':'、IPv4 は数字 + '.' を使う。
// isIPByte reports whether c can appear in an IP candidate: hex digit, ':'
// or '.'. IPv6 uses hex + ':', IPv4 uses digits + '.'.
func isIPByte(c byte) bool {
	return (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'f') ||
		(c >= 'A' && c <= 'F') ||
		c == ':' || c == '.'
}

// ExtractIPs は s に現れるすべての IP アドレスを出現順に、重複を除いて返す。
// IPv4 アドレスの直後に ":port" が続く場合はポートを除いて返す。未指定アドレスと
// ブロードキャストアドレスは落とす。これらは bind/listen アドレスであり、
// ピアではない（"TCP ポート 44444 が … （0.0.0.0）"）。
//
// IP 形状のバイト（16 進数字、':'、'.'）の最大連続を走査し、その連続に対してのみ
// net.ParseIP を呼ぶ。連続は非 IP バイト（または文字列端）に挟まれている必要があり、
// "abc1.2.3.4" はアドレスとして扱わない。これは正規表現バックトラックの無い
// 単一の線形パスだ。5+ MB の 16 進っぽいテキストの行も 1 秒未満で解析でき、
// 各候補は 1 回だけ解析する（以前の正規表現版は 'a' の連続に対して約 N/45 個の
// 候補を生成し、各々を 2 回解析していた）。
//
// 注意: メッセージ本文からの抽出なので、「接続元」以外の IP（待ち受け
// アドレス等）を拾う可能性が残る。集計のヒントとして使い、断定には使わない。
// また、16進数の語に直接埋もれた IP（例 "abc1.2.3.4"）は取りこぼす。
// ExtractIPs returns every IP address that appears in s, in order of
// appearance, de-duplicated. An IPv4 address followed by ":port" is returned
// without the port. Unspecified and broadcast addresses are dropped: they are
// bind/listen addresses, not peers ("TCP ポート 44444 が … （0.0.0.0）").
//
// It scans for maximal runs of IP-shaped bytes (hex digits, ':' and '.') and
// only calls net.ParseIP on those runs. A run must be flanked by a non-IP byte
// (or a string end), so "abc1.2.3.4" is not treated as an address. This is a
// single linear pass with no regexp backtracking: a 5+ MB log line of hex-ish
// text parses in well under a second, and each candidate is parsed once (the
// previous regexp version produced ~N/45 candidates for a run of 'a's and
// parsed each twice).
//
// 注意: メッセージ本文からの抽出なので、「接続元」以外の IP（待ち受け
// アドレス等）を拾う可能性が残る。集計のヒントとして使い、断定には使わない。
// また、16進数の語に直接埋もれた IP（例 "abc1.2.3.4"）は取りこぼす。
func ExtractIPs(s string) []string {
	var out []string
	seen := map[string]bool{}
	i := 0
	n := len(s)
	for i < n {
		if !isIPByte(s[i]) {
			i++
			continue
		}
		start := i
		for i < n && isIPByte(s[i]) {
			i++
		}
		// 連続は非 IP バイト（または文字列端）に挟まれている必要がある。
		// これにより、より大きな 16 進トークンから切り出された断片
		// （例 "abc1.2.3.4"）とはならない。
		// A run must be bounded by non-IP bytes (or the string ends), so it is
		// not a fragment cut out of a larger hex token (e.g. "abc1.2.3.4").
		if start > 0 && isIPByte(s[start-1]) {
			continue
		}
		if i < n && isIPByte(s[i]) {
			continue
		}
		run := s[start:i]
		if len(run) < 3 || len(run) > 45 {
			continue
		}
		cand := strings.Trim(run, ".")
		if cand == "" {
			continue
		}
		ip := parseIPCandidate(cand)
		if ip == "" || isUnspecifiedOrBroadcast(ip) {
			continue
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	return out
}

// parseIPCandidate は素のアドレスまたは IPv4 の ":port" 組を受け付け、
// 正規化したアドレスを返す。候補がアドレスでなければ "" を返す。
// parseIPCandidate accepts a bare address or an IPv4 ":port" pair and returns
// the canonical address, or "" when the candidate is not an address.
func parseIPCandidate(cand string) string {
	if ip := net.ParseIP(cand); ip != nil {
		return ip.String()
	}
	// IPv4:port — ポートを剥がして再試行する。コロン 1 つだけを考慮するため、
	// 角括弧なしの IPv6 候補を誤分割しない。
	// IPv4:port — strip the port and retry. Only a single colon is considered,
	// so an unbracketed IPv6 candidate is not mis-split.
	if i := strings.LastIndexByte(cand, ':'); i > 0 && !strings.Contains(cand[:i], ":") {
		if ip := net.ParseIP(cand[:i]); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// isUnspecifiedOrBroadcast は s が 0.0.0.0 / :: / 255.255.255.255 かを返す。
// 解析できない値も拒否する（防御的）。
// isUnspecifiedOrBroadcast reports whether s is 0.0.0.0 / :: / 255.255.255.255.
// A value that does not parse is also rejected (defensive).
func isUnspecifiedOrBroadcast(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() || ip.Equal(net.IPv4bcast)
}

// ---------------------------------------------------------------------------
// Threat (期間リスク指数)
// Threat (period risk index)
// ---------------------------------------------------------------------------

// Threat は期間リスク指数だ。
// Threat is the period risk index.
type Threat struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
	Success  int `json:"success"`

	// Risk は 0..100 で、高いほどリスクが高い。選択期間の累積指数であり、
	// パーセントではなく、異なる長さの窓と比較できるものでもない。
	// Risk is 0..100, higher = more risk. It is a cumulative index for the
	// selected period, not a percentage and not comparable across windows of
	// different length.
	Risk  int    `json:"risk"`
	Grade string `json:"grade"`

	// Drivers は Risk に最も寄与したカテゴリだ（weight > 0）。
	// Drivers are the categories that contributed most to Risk (weight > 0).
	Drivers []Driver `json:"drivers"`

	// Sources は窓内で見た異なる送信元アドレスの数だ。
	// Sources is the number of distinct source addresses seen in the window.
	Sources int `json:"sources"`
}

// Driver は 1 カテゴリのリスク指数への寄与だ。
// Driver is one category's contribution to the risk index.
type Driver struct {
	Key      string `json:"key"`
	Count    int    `json:"count"`
	Critical int    `json:"critical"`
	Weight   int    `json:"weight"`
}

// ---------------------------------------------------------------------------
// Profile (攻撃者プロファイリング)
// Profile (attacker profiling)
// ---------------------------------------------------------------------------

// Profile は窓を送信元アドレス、手法、時間帯で集計する。
// Profile aggregates the window by source address, technique and hour.
type Profile struct {
	Sources    []SourceStat   `json:"sources"`
	Categories []CategoryStat `json:"categories"`
	// Hourly は記録された時間帯（0-23、各アラートが保存されたタイムゾーン）で
	// アラートを数える。
	// Hourly counts alerts by the hour they were recorded (0-23, in the
	// timezone each alert was stored with).
	Hourly [24]int `json:"hourly"`
	// PeakHour は Hourly で最も忙しい時間帯、データが無ければ -1 だ。
	// PeakHour is the busiest hour in Hourly, or -1 when there is no data.
	PeakHour int `json:"peak_hour"`
}

// SourceStat は 1 つの送信元アドレスの窓内の活動だ。
// SourceStat is one source address's activity in the window.
type SourceStat struct {
	IP        string    `json:"ip"`
	Count     int       `json:"count"`
	Critical  int       `json:"critical"`
	Warning   int       `json:"warning"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Categories はこのアドレスからの異なる手法の集合だ（上限付き）。
	// Categories is the distinct set of techniques from this address (capped).
	Categories []string `json:"categories"`
	// MultiTechnique はこのアドレスが 2 つ以上の手法を使ったとき true になる。
	// これは単一の設定ミスしたクライアントよりも、意図的な試みと整合する。
	// MultiTechnique is true when the address used two or more techniques,
	// which is more consistent with a deliberate attempt than a single
	// misconfigured client.
	MultiTechnique bool `json:"multi_technique"`
}

// CategoryStat は 1 つの手法の窓内の活動だ。
// CategoryStat is one technique's activity in the window.
type CategoryStat struct {
	Key      string `json:"key"`
	Count    int    `json:"count"`
	Critical int    `json:"critical"`
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// Result は Analyze が生成するすべてだ。
// Result is everything Analyze produces.
type Result struct {
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until"`
	Period int64     `json:"period_sec"`
	Threat Threat    `json:"threat"`
	// Profile はポインタではなく値だ。これにより JSON 形状が「データ無し」と
	// 「データ有り」で決して変わらない。
	// Profile is a value, not a pointer, so the JSON shape never changes
	// between "no data" and "data".
	Profile Profile `json:"profile"`
}

// Analyze は [now-period, now] に入るアラートエントリを集計する。エントリは
// 任意の順序で受け付ける。now がゼロなら time.Now()、period が非正なら
// DefaultPeriod を使う。
// Analyze aggregates the alert entries that fall inside [now-period, now].
// Entries are accepted in any order. A zero now means time.Now(); a
// non-positive period means DefaultPeriod.
func Analyze(entries []alert.HistoryEntry, now time.Time, period time.Duration) Result {
	if now.IsZero() {
		now = time.Now()
	}
	if period <= 0 {
		period = DefaultPeriod
	}
	since := now.Add(-period)

	res := Result{Since: since, Until: now, Period: int64(period.Seconds())}
	res.Profile.PeakHour = -1
	res.Profile.Sources = []SourceStat{}
	res.Profile.Categories = []CategoryStat{}
	res.Threat.Drivers = []Driver{}

	byCategory := map[string]*Driver{}
	byCategoryStat := map[string]*CategoryStat{}
	bySource := map[string]*SourceStat{}
	bySourceCats := map[string]map[string]bool{}

	risk := 0
	for i := range entries {
		e := entries[i]
		if e.Timestamp.IsZero() || e.Timestamp.Before(since) {
			continue
		}

		key := CategoryOf(e.Type)
		lvl := strings.ToLower(strings.TrimSpace(e.Level))
		w := levelWeight(lvl)
		risk += w

		res.Threat.Total++
		switch lvl {
		case "critical":
			res.Threat.Critical++
		case "warning":
			res.Threat.Warning++
		case "success":
			res.Threat.Success++
		default:
			res.Threat.Info++
		}

		d := byCategory[key]
		if d == nil {
			d = &Driver{Key: key}
			byCategory[key] = d
		}
		d.Count++
		d.Weight += w
		if lvl == "critical" {
			d.Critical++
		}

		c := byCategoryStat[key]
		if c == nil {
			c = &CategoryStat{Key: key}
			byCategoryStat[key] = c
		}
		c.Count++
		if lvl == "critical" {
			c.Critical++
		}

		res.Profile.Hourly[e.Timestamp.Hour()]++

		for _, ip := range ExtractIPs(e.Message) {
			s := bySource[ip]
			if s == nil {
				s = &SourceStat{IP: ip, FirstSeen: e.Timestamp, LastSeen: e.Timestamp}
				bySource[ip] = s
				bySourceCats[ip] = map[string]bool{}
			}
			s.Count++
			switch lvl {
			case "critical":
				s.Critical++
			case "warning":
				s.Warning++
			}
			if e.Timestamp.Before(s.FirstSeen) {
				s.FirstSeen = e.Timestamp
			}
			if e.Timestamp.After(s.LastSeen) {
				s.LastSeen = e.Timestamp
			}
			bySourceCats[ip][key] = true
		}
	}

	if risk > 100 {
		risk = 100
	}
	res.Threat.Risk = risk
	res.Threat.Grade = Grade(100 - risk)
	res.Threat.Sources = len(bySource)
	res.Threat.Drivers = topDrivers(byCategory)
	res.Profile.Categories = topCategories(byCategoryStat)
	res.Profile.Sources = topSources(bySource, bySourceCats)
	res.Profile.PeakHour = peakHour(res.Profile.Hourly)
	return res
}

// Grade は 0..100 の「良さ」値（100 = 最良）を文字へ対応付ける。リスク指数
// （良さ = 100-risk）とポスチャスコアの両方がこれを使うため、2 つは同じように
// 読める。
// Grade maps a 0..100 "goodness" value (100 = best) to a letter. Both the
// risk index (goodness = 100-risk) and the posture score use it, so the two
// are read the same way.
func Grade(goodness int) string {
	switch {
	case goodness >= 90:
		return "A"
	case goodness >= 75:
		return "B"
	case goodness >= 60:
		return "C"
	case goodness >= 40:
		return "D"
	default:
		return "E"
	}
}

// topDrivers は重みの高いカテゴリを、影響の大きい順に返す。重みゼロのカテゴリ
// （成功/復旧アラートのみ）は省略する。リスクを運ばず、drivers リストを
// 誤って表現してしまうためだ。
// topDrivers returns the highest-weight categories, most impactful first.
// Categories with zero weight (only success/recovery alerts) are omitted:
// they carry no risk and would misrepresent the drivers list.
func topDrivers(m map[string]*Driver) []Driver {
	out := make([]Driver, 0, len(m))
	for _, d := range m {
		if d.Weight <= 0 {
			continue
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxList {
		out = out[:maxList]
	}
	return out
}

// topCategories は最も頻繁なカテゴリを、忙しい順に返す。
// topCategories returns the most frequent categories, busiest first.
func topCategories(m map[string]*CategoryStat) []CategoryStat {
	out := make([]CategoryStat, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Critical != out[j].Critical {
			return out[i].Critical > out[j].Critical
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxList {
		out = out[:maxList]
	}
	return out
}

// topSources は最も忙しい送信元アドレスを、重大な順に返す。アドレスごとの手法
// 集合はここで（上限付きで）添付し、UI が 2 回目の集計パス無しに表示できるようにする。
// topSources returns the busiest source addresses, most severe first. The
// per-address technique set is attached here (capped) so the UI can show it
// without a second aggregation pass.
func topSources(bySource map[string]*SourceStat, cats map[string]map[string]bool) []SourceStat {
	out := make([]SourceStat, 0, len(bySource))
	for ip, s := range bySource {
		cp := *s
		keys := make([]string, 0, len(cats[ip]))
		for k := range cats[ip] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > maxList {
			keys = keys[:maxList]
		}
		cp.Categories = keys
		cp.MultiTechnique = len(cats[ip]) >= 2
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Critical != out[j].Critical {
			return out[i].Critical > out[j].Critical
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].IP < out[j].IP
	})
	if len(out) > maxList {
		out = out[:maxList]
	}
	return out
}

// peakHour は最も忙しい時間帯を返し、ヒストグラムが空なら -1 を返す。
// peakHour returns the busiest hour, or -1 when the histogram is empty.
func peakHour(h [24]int) int {
	best := -1
	for i, n := range h {
		if n > 0 && (best < 0 || n > h[best]) {
			best = i
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Posture (設定チェックリストのスコア)
// Posture (configuration checklist score)
// ---------------------------------------------------------------------------

// PostureInput はポスチャスコアを計算する元になる設定状態だ。
// *config.DashboardConfig ではなく素の struct にしてあるため、このパッケージは
// 設定結合から自由で、テストが自明になる。呼び出し側（internal/api）が
// 読み込んだ設定から埋める。
//
// フィールドが bool なのは意図的だ。「この制御は有効か?」が採点対象であり、
// スコアは単一のスナップショットから再現できなければならない。
//
// 注意: これは設定の点検結果であって、稼働中の脅威ではない。攻撃の有無は
// Threat / Profile 側が示す。
// PostureInput is the configuration state the posture score is computed from.
// It is a plain struct rather than *config.DashboardConfig so this package
// stays free of configuration coupling and is trivial to test. The caller
// (internal/api) fills it from the loaded configs.
//
// A field is a bool on purpose: "is this control on?" is what is scored, and
// the score must be reproducible from a single snapshot.
//
// 注意: これは設定の点検結果であって、稼働中の脅威ではない。攻撃の有無は
// Threat / Profile 側が示す。
type PostureInput struct {
	AuthEnabled          bool
	PublicViewerEnabled  bool
	SecureCookies        bool
	AgentTokenSet        bool
	RequireSignature     bool
	PublicKeySet         bool
	AlertHistorySigned   bool
	NotificationsEnabled bool
	// ListenRestricted は dashboard が loopback のみで待ち受けるとき true だ
	// （リバースプロキシ背後の推奨構成）。
	// ListenRestricted is true when the dashboard listens on loopback only
	// (the recommended setup behind a reverse proxy).
	ListenRestricted bool
}

// Posture は設定チェックリストのスコアだ（高いほど良い）。
// Posture is the configuration checklist score (higher = better).
type Posture struct {
	Score int           `json:"score"`
	Max   int           `json:"max"`
	Grade string        `json:"grade"`
	Items []PostureItem `json:"items"`
}

// PostureItem はチェックリストの 1 項目だ。Key は安定しており、UI が翻訳する
// （posture.<key>）。これにより文言を API を壊さずに変えられる。API は意図的に
// 散文を返さない。i18n はフロントエンドの領分だ。
// PostureItem is one checklist entry. Key is stable and is translated by the
// UI (posture.<key>), so the wording can change without breaking the API.
// The API deliberately returns no prose: i18n belongs to the frontend.
type PostureItem struct {
	Key    string `json:"key"`
	Weight int    `json:"weight"`
	OK     bool   `json:"ok"`
}

// EvaluatePosture は設定を採点する。重みの合計は 100 になる。
// EvaluatePosture scores the configuration. The weights sum to 100.
func EvaluatePosture(in PostureInput) Posture {
	items := []PostureItem{
		{Key: "auth_enabled", Weight: 25, OK: in.AuthEnabled},
		{Key: "require_signature", Weight: 20, OK: in.RequireSignature && in.PublicKeySet},
		{Key: "public_viewer_off", Weight: 10, OK: !in.PublicViewerEnabled},
		{Key: "agent_token", Weight: 10, OK: in.AgentTokenSet},
		{Key: "alert_history_signed", Weight: 10, OK: in.AlertHistorySigned},
		{Key: "notifications_enabled", Weight: 10, OK: in.NotificationsEnabled},
		{Key: "listen_restricted", Weight: 10, OK: in.ListenRestricted},
		{Key: "secure_cookies", Weight: 5, OK: in.SecureCookies},
	}

	p := Posture{Items: items}
	for _, it := range items {
		p.Max += it.Weight
		if it.OK {
			p.Score += it.Weight
		}
	}
	p.Grade = Grade(p.Score)
	return p
}
