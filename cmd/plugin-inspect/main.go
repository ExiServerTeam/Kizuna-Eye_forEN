// cmd/plugin-inspect/main.go（プラグイン検査ツール）
// cmd/plugin-inspect/main.go (plugin inspection tool)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"plugin"
	"reflect"
	"runtime/debug"
	"strings"
	"time"

	"Kizuna-Eye/pkg/fsutil"
)

// InspectResult は plugin-inspect が吐き出す JSON のルートだ。
// InspectResult is the root of the JSON that plugin-inspect emits.
type InspectResult struct {
	Success     bool   `json:"success"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// Tag はプラグインが Tag() string で宣言する短いバッジだ
	// （例: "GUARD" / "LITE" / "PRO"）。プラグインが宣言しない場合は空で、
	// UI は "PLUGIN" を表示する。
	// Tag is the short badge the plugin declares via Tag() string
	// (e.g. "GUARD" / "LITE" / "PRO"). Empty when the plugin does not
	// declare one; the UI then shows "PLUGIN".
	Tag         string          `json:"tag,omitempty"`
	Description string          `json:"description"`
	IntervalSec float64         `json:"interval_sec"`
	Fields      json.RawMessage `json:"fields"`
	IsBackup    bool            `json:"is_backup"`
	// DashboardCards はモジュールの DashboardCards() 戻り値の JSON 形式
	// （{id,title,title_en,kind,min_role} の配列）で、モジュールが何も宣言しなければ
	// 省略される。dashboard はこれを読み、モジュールが提供するカードを描画するため、
	// カードはハードコードではなくモジュールが追加する。
	// DashboardCards is the JSON form of the module's DashboardCards() return
	// (an array of {id,title,title_en,kind,min_role}), or omitted when the
	// module declares none. The dashboard reads it to render the card the
	// module contributes, so the card is added by the module, not hardcoded.
	DashboardCards json.RawMessage `json:"dashboard_cards,omitempty"`
	// WebUIPath はモジュールの UI ルートからの相対のエントリ文書
	// （WebAssetProvider 由来）、UI を同梱しない場合は空だ。
	// WebUIPath is the module's entry document relative to its UI root (from
	// WebAssetProvider), or empty when it ships no UI.
	WebUIPath string `json:"web_ui_path,omitempty"`
	// WebAssetsCount は WebAssets() が返したファイル数だ（0 = 無し）。
	// WebAssetsCount is how many files WebAssets() returned (0 = none).
	WebAssetsCount int `json:"web_assets_count,omitempty"`
	// WebAssetsDir はアセットの展開先ディレクトリだ（--web-out 指定時のみ設定）。
	// WebAssetsDir is the directory the assets were extracted to (set only
	// when --web-out was given).
	WebAssetsDir string `json:"web_assets_dir,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	PanicStack   string `json:"panic_stack,omitempty"`
	LoadedAt     string `json:"loaded_at"`
}

func main() {
	var (
		soPath  = flag.String("so", "", "対象の .so ファイルパス")
		pretty  = flag.Bool("pretty", true, "JSON を整形して出力")
		timeout = flag.Duration("timeout", 5*time.Second, "タイムアウト")
		webOut  = flag.String("web-out", "", "WebAssets() を展開するディレクトリ（省略時は展開しない）")
	)
	flag.Parse()

	if *soPath == "" {
		writeError("--so が指定されていません", *pretty, "")
		os.Exit(1)
	}

	done := make(chan struct{})
	var result InspectResult

	go func() {
		defer close(done)
		result = inspect(*soPath, *webOut)
	}()

	select {
	case <-done:
	case <-time.After(*timeout):
		writeError("inspect がタイムアウトしました", *pretty, "")
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "JSON 出力失敗: %v\n", err)
		os.Exit(1)
	}

	if !result.Success {
		os.Exit(1)
	}
}

// inspect はリフレクションベースで .so の情報を抽出する。
// webOutDir が空でなければ WebAssets() の内容をそのディレクトリへ展開する。
// inspect extracts .so information via reflection.
// When webOutDir is non-empty, the contents of WebAssets() are extracted there.
func inspect(soPath, webOutDir string) (res InspectResult) {
	res = InspectResult{
		LoadedAt: time.Now().Format(time.RFC3339),
	}

	defer func() {
		if r := recover(); r != nil {
			res.Success = false
			res.ErrorMessage = fmt.Sprintf("パニックを検出: %v", r)
			res.PanicStack = string(debug.Stack())
		}
	}()

	p, err := plugin.Open(soPath)
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("plugin.Open 失敗: %v", err)
		return res
	}

	sym, err := p.Lookup("Plugin")
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("シンボル Plugin が見つかりません: %v", err)
		return res
	}

	// Lookup は変数のアドレスを返すため、Elem() でポインタを剥がす。
	// Lookup returns the variable address, so unwrap the pointer with Elem().
	v := reflect.ValueOf(sym)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// --- GetConfigFields() をリフレクションで呼ぶ ---
	method := v.MethodByName("GetConfigFields")
	if !method.IsValid() {
		res.ErrorMessage = "Plugin は GetConfigFields メソッドを実装していません"
		return res
	}
	// 敵対的な .so が引数付きの GetConfigFields を宣言すると Call(nil) が
	// panic するため、それに備える。
	// Guard against a hostile .so declaring GetConfigFields with parameters
	// (Call(nil) would panic).
	if method.Type().NumIn() != 0 || method.Type().NumOut() != 1 {
		res.ErrorMessage = "GetConfigFields のシグネチャが不正です（引数なし・戻り値1つが必要）"
		return res
	}

	results := method.Call(nil)
	if len(results) == 0 {
		res.ErrorMessage = "GetConfigFields が戻り値を返しませんでした"
		return res
	}

	fieldsJSON, err := json.Marshal(results[0].Interface())
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("GetConfigFields の戻り値の JSON 化失敗: %v", err)
		return res
	}
	res.Fields = json.RawMessage(fieldsJSON)
	res.Success = true

	// --- リフレクションで追加情報を取得する ---
	// --- Get extra info via reflection ---
	if s := callString(v, "Name"); s != "" {
		res.Name = s
	}
	if s := callString(v, "DisplayName"); s != "" {
		res.DisplayName = s
	}
	if s := callString(v, "Description"); s != "" {
		res.Description = s
	}
	if s := callString(v, "Tag"); s != "" {
		res.Tag = s
	}
	if d := callDurationSec(v, "Interval"); d > 0 {
		res.IntervalSec = d
	}
	if v.MethodByName("RunBackup").IsValid() {
		res.IsBackup = true
	}
	// DashboardCards() は汎用的に marshal する（その具象スライス型はこのバイナリの
	// module パッケージと異なり得るため型アサーションは使えない。json.Marshal は
	// JSON タグ経由でリフレクションを通じて動く）。
	// DashboardCards() is marshalled generically (its concrete slice type may
	// differ from this binary's module package, so a type assertion is not
	// usable; json.Marshal works through reflection via the JSON tags).
	if cards := callJSON(v, "DashboardCards"); cards != nil {
		res.DashboardCards = cards
	}
	// Web UI: モジュールは独自の HTML/JS/CSS を同梱する（設計 5.4）。エントリ文書名と
	// アセットマップはリフレクションで読み、呼び出し側が要求すればアセットを
	// webOutDir へ展開する。
	// Web UI: the module ships its own HTML/JS/CSS (design 5.4). The entry
	// document name and the asset map are read by reflection; the assets are
	// extracted to webOutDir when the caller asked for it.
	extractWebAssets(v, &res, webOutDir)

	if res.Name == "" {
		res.Name = deriveNameFromPath(soPath)
	}

	return res
}

// callString は引数なし・戻り値 string のメソッドを呼ぶ。
// 敵対的な .so が引数付きの同名メソッドを宣言すると Call(nil) が panic する。
// まずシグネチャを検証し、最後の手段として recover し、1 つの不正メソッドが
// 検査全体を中断しないようにする。
// callString calls a no-argument method returning a single string.
// A hostile .so could declare a same-named method with parameters, which would
// make Call(nil) panic. Verify the signature first and recover as a last
// resort so one bad method does not abort the whole inspection.
func callString(v reflect.Value, methodName string) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	m := v.MethodByName(methodName)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		return ""
	}
	results := m.Call(nil)
	if len(results) != 1 {
		return ""
	}
	s, _ := results[0].Interface().(string)
	return s
}

// callDurationSec は引数なし・戻り値 time.Duration のメソッドを秒に変換して返す。
// callDurationSec calls a no-argument method returning time.Duration and
// converts it to seconds.
func callDurationSec(v reflect.Value, methodName string) (out float64) {
	defer func() {
		if recover() != nil {
			out = 0
		}
	}()
	m := v.MethodByName(methodName)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		return 0
	}
	results := m.Call(nil)
	if len(results) != 1 {
		return 0
	}
	raw := results[0].Interface()
	if d, ok := raw.(time.Duration); ok {
		return d.Seconds()
	}
	if i, ok := raw.(int64); ok {
		return time.Duration(i).Seconds()
	}
	return 0
}

// callJSON は引数なしメソッドを呼び、その単一の戻り値を JSON へ marshal して返す。
// プラグインとこのバイナリで具象型が異なり得る共有 struct のスライスに使う
// （Go プラグインの型同一性はビルド単位）。そのため型アサーションは失敗するが
// json.Marshal は成功する。メソッドが無い、シグネチャが誤っている、または値が
// 空配列へ marshal される場合は nil を返す。
// callJSON calls a no-argument method and returns its single return value
// marshalled to JSON. It is used for slices of shared structs whose concrete
// type may differ between the plugin and this binary (Go plugin type identity
// is per-build), so a type assertion would fail but json.Marshal succeeds.
// Returns nil when the method is missing, has the wrong signature, or the
// value marshals to an empty array.
func callJSON(v reflect.Value, methodName string) (out json.RawMessage) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	m := v.MethodByName(methodName)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		return nil
	}
	results := m.Call(nil)
	if len(results) != 1 {
		return nil
	}
	// nil スライスは "null" へ marshal されるため、「何も宣言されていない」と扱う。
	// A nil slice marshals to "null"; treat that as "none declared".
	if results[0].Kind() == reflect.Slice && results[0].IsNil() {
		return nil
	}
	b, err := json.Marshal(results[0].Interface())
	if err != nil {
		return nil
	}
	if string(b) == "[]" || string(b) == "null" {
		return nil
	}
	return json.RawMessage(b)
}

// extractWebAssets はリフレクションで WebUIPath()/WebAssets() を読み、outDir が
// 設定されていればアセットを outDir 下へ（パスごとに 1 ファイル）書き出す。
// パスは検証し、絶対パス・".." トラバーサル・バックスラッシュを拒否する。
// これにより敵対的な .so が outDir の外へ書けない。UI を同梱しないモジュール
// （メソッド無し、または空マップ）は何も設定しない。
// extractWebAssets reads WebUIPath()/WebAssets() by reflection and, when
// outDir is set, writes the assets under outDir (one file per path). Paths
// are validated: absolute paths, ".." traversal and backslashes are rejected,
// so a hostile .so cannot write outside outDir. A module that ships no UI
// (no methods, or an empty map) sets nothing.
func extractWebAssets(v reflect.Value, res *InspectResult, outDir string) {
	// エントリ文書（string）。
	// Entry document (string).
	if s := callString(v, "WebUIPath"); s != "" {
		res.WebUIPath = s
	}

	// アセットマップ（map[string][]byte）。汎用的に読む。具象型はプラグインと
	// このバイナリで異なり得るため型アサーションは使えない。マップ上の
	// リフレクションが両方を扱う。
	// Asset map (map[string][]byte). Read generically: the concrete type may
	// differ between the plugin and this binary, so a type assertion is not
	// usable; reflection over the map handles both.
	assets := callBytesMap(v, "WebAssets")
	if len(assets) == 0 {
		return
	}
	res.WebAssetsCount = len(assets)

	if outDir == "" {
		return
	}
	base, err := filepath.Abs(outDir)
	if err != nil {
		return
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return
	}
	for rel, content := range assets {
		clean, ok := safeRelPath(rel)
		if !ok {
			continue
		}
		target := filepath.Join(base, clean)
		// 多層防御: join したパスは base の下に留まらなければならない。
		// Defense in depth: the joined path must stay under base.
		if !strings.HasPrefix(target, base+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		// fsutil.WriteFileAtomic は最終パス要素の symlink を拒否し、
		// temp+fsync+rename で書く。展開先ディレクトリは pluginsDir 下
		// （agent ユーザーが書き込める）にあるため、そうしないと仕込まれた
		// symlink が書き込み先を任意ファイルへ誘導し得る（CWE-59）。
		// 既定で使う bubblewrap 隔離は残りのファイルシステムを読み取り専用で
		// バインドするが、展開自体が単独で安全でなければならない
		// （配備が inspect_isolation=off にし得る）。
		// fsutil.WriteFileAtomic rejects a symlink at the final path component
		// and writes via temp+fsync+rename. The extraction directory lives
		// under pluginsDir (writable by the agent user), so a symlink planted
		// there would otherwise redirect the write to an arbitrary file
		// (CWE-59). The bubblewrap isolation used by default binds the rest of
		// the filesystem read-only, but the extraction must stay safe on its
		// own (a deployment may set inspect_isolation=off).
		_ = fsutil.WriteFileAtomic(target, content, 0o644)
	}
	res.WebAssetsDir = base
}

// safeRelPath は UI 相対パスを正規化し、UI ルートを脱出し得るもの
// （絶対パス、".."、バックスラッシュ）を拒否する。正規化済みの相対パス
// （"/" 区切り入力を使用）を返す。
// safeRelPath normalizes a UI-relative path and rejects anything that could
// escape the UI root: absolute paths, "..", and backslashes. Returns the
// cleaned relative path (using "/"-separated input).
func safeRelPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return "", false
	}
	if strings.Contains(p, "\\") {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// callBytesMap は引数なし・単一の戻り値が map[string][]byte のメソッドを呼び、
// 素の Go マップとして返す。メソッドが無い、シグネチャが誤っている、または
// マップが空の場合は nil を返す。
// callBytesMap calls a no-argument method whose single return value is a
// map[string][]byte and returns it as a plain Go map. Returns nil when the
// method is missing, has the wrong signature, or the map is empty.
func callBytesMap(v reflect.Value, methodName string) (out map[string][]byte) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	m := v.MethodByName(methodName)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		return nil
	}
	results := m.Call(nil)
	if len(results) != 1 {
		return nil
	}
	rv := results[0]
	if rv.Kind() != reflect.Map {
		return nil
	}
	if rv.IsNil() || rv.Len() == 0 {
		return nil
	}
	// 呼び出し側がプラグインのマップを保持しないよう、具象マップへコピーする。
	// Copy into a concrete map so callers do not hold the plugin's map.
	out = make(map[string][]byte, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		k := iter.Key()
		val := iter.Value()
		if k.Kind() != reflect.String || val.Kind() != reflect.Slice {
			continue
		}
		out[k.String()] = append([]byte(nil), val.Bytes()...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func deriveNameFromPath(soPath string) string {
	base := soPath
	// "/"（POSIX）と "\\"（Windows）の両方をパス区切りとして扱う。
	// Treat both "/" (POSIX) and "\\" (Windows) as path separators.
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".so")
	return base
}

func writeError(msg string, pretty bool, stack string) {
	res := InspectResult{
		Success:      false,
		ErrorMessage: msg,
		PanicStack:   stack,
		LoadedAt:     time.Now().Format(time.RFC3339),
	}
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(res)
}
