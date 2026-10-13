// configsnap パッケージは設定ファイルのタイムスタンプ付きスナップショットを保ち、
// 運用者が任意の過去の時点へロールバック（「タイムトラベル」）できるようにする。
//
// 設計意図:
//   - 保存（設定変更）の直前に、現行ファイルをスナップショットとして退避する。
//   - 世代管理は GFS 的（直近 N 件 + 日次 + 週次 + 月次）に行い、古いものから削除する。
//   - 退避先は設定ファイルと同じディレクトリ配下の snapshots/（Samba 共有外）。
//     秘密を含むため 0600、ディレクトリは 0700。
//   - 復元は原子的（fsutil.WriteFileAtomic）。復元前の現行は「上書きされる
//     1 スロット」だけに退避する（復元は設定変更ではないので、履歴を増やさない）。
//
// Package configsnap keeps timestamped snapshots of configuration files so an
// operator can roll back to any earlier point ("time travel").
//
// 設計意図:
//   - 保存（設定変更）の直前に、現行ファイルをスナップショットとして退避する。
//   - 世代管理は GFS 的（直近 N 件 + 日次 + 週次 + 月次）に行い、古いものから削除する。
//   - 退避先は設定ファイルと同じディレクトリ配下の snapshots/（Samba 共有外）。
//     秘密を含むため 0600、ディレクトリは 0700。
//   - 復元は原子的（fsutil.WriteFileAtomic）。復元前の現行は「上書きされる
//     1 スロット」だけに退避する（復元は設定変更ではないので、履歴を増やさない）。
package configsnap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
)

const (
	// dirName はスナップショットを保持する設定ファイルのサブディレクトリだ。
	// dirName is the sub-directory of the config file that holds snapshots.
	dirName = "snapshots"

	// timeLayout はミリ秒を含み、同じ秒の 2 回の保存が衝突しないようにする。
	// 同じレイアウトでパースし直す。
	// timeLayout includes milliseconds so two saves in the same second do not
	// collide. It is parsed back in the same layout.
	timeLayout = "20060102T150405.000"

	// GFS 保持の既定値。
	// GFS retention defaults.
	keepRecent  = 20 // 直近の保存を無条件で保持 / keep the most recent saves unconditionally
	keepDaily   = 7  // 直近7日は 1日1件 / one per day for the last 7 days
	keepWeekly  = 8  // 直近8週は 1週1件 / one per week for the last 8 weeks
	keepMonthly = 12 // 直近12か月は 1月1件 / one per month for the last 12 months
)

// mu は snapshot/list/restore を直列化し、並行する保存がスナップショット
// ディレクトリ上で競合しない（そして prune が復元中のファイルを消さない）ようにする。
// mu serializes snapshot/list/restore so concurrent saves cannot race on the
// snapshot directory (and so prune never removes a file mid-restore).
var mu sync.Mutex

// Entry は退避ファイル 1 件のメタデータだ。
// Seq は同じミリ秒に取ったスナップショットを区別する（0 = 最初）。Time と
// 併せて順序付けに使い、タイムスタンプが同一でも保存のバーストが真の順序を保つ。
// Entry is one退避ファイルのメタデータ。
// Seq disambiguates snapshots taken in the same millisecond (0 = first). It
// is used for ordering together with Time, so a burst of saves keeps its
// true order even when the timestamp is identical.
type Entry struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
	Seq  int       `json:"seq,omitempty"`
	Size int64     `json:"size"`
}

// snapDir は設定ファイルのスナップショットを保持するディレクトリを返す。
// ベース名は拡張子を剥がす（dashboard_config.json ではなく dashboard_config）。
// 拡張子を保つとディレクトリがファイルに見え、"dashboard_config/" を探す
// 運用者を混乱させた。
// snapDir returns the directory that holds snapshots for a config file.
// The base name is stripped of its extension (dashboard_config, not
// dashboard_config.json): keeping the extension made the directory look like a
// file and confused operators looking for "dashboard_config/".
func snapDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), dirName, baseName(configPath))
}

// baseName は設定ファイルのベース名を拡張子なしで返す。
// baseName returns the config file's base name without extension.
func baseName(configPath string) string {
	b := filepath.Base(configPath)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// Snapshot は現在の設定ファイルをスナップショットディレクトリへコピーする。
// ファイル欠落は no-op だ（スナップショットするものが無い）。
// Snapshot copies the current config file into the snapshot directory. A missing
// file is a no-op (nothing to snapshot).
func Snapshot(configPath string) error {
	if configPath == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	return snapshotLocked(configPath, time.Now().UTC())
}

// snapshotLocked はコピー + prune を行う。呼び出し側は mu を保持すること。
// snapshotLocked performs the copy + prune. Caller must hold mu.
func snapshotLocked(configPath string, now time.Time) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dir := snapDir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	name := uniqueName(dir, baseName(configPath), now)
	target := filepath.Join(dir, name)
	// 0600: snapshot は秘密を含む。WriteFileAtomic は symlink を拒否する。
	// 0600: snapshots hold secrets. WriteFileAtomic refuses a symlink.
	if err := fsutil.WriteFileAtomic(target, data, 0600); err != nil {
		return err
	}
	return pruneLocked(configPath, now)
}

// preRestoreName は直近の Restore の直前の設定内容を保持する、固定で上書きされる
// スロットだ。意図的にタイムスタンプ付きスナップショットにはしない。List() は
// "<base>.<ts>.json" のみを認識するため、このファイルは履歴に決して現れない。
// 復元は設定変更ではないので、エントリを蓄積してはならない（繰り返しの復元が
// そうでなければ運用者の本当の保存を GFS 窓から押し出してしまう）。最新の
// 復元前状態だけが有用なので、毎回上書きする。
// preRestoreName is the fixed, overwritten slot that holds the config content
// from just before the most recent Restore. It is deliberately NOT a
// timestamped snapshot: List() only recognizes "<base>.<ts>.json", so this
// file never appears in the history. A restore is not a config change, so it
// must not accumulate entries (repeated restores would otherwise push the
// operator's real saves out of the GFS window). Only the most recent
// pre-restore state is useful, so it is overwritten each time.
const preRestoreName = ".pre-restore.json"

// preRestorePath は復元前スロットのパスを返す。
// preRestorePath returns the path of the pre-restore slot.
func preRestorePath(configPath string) string {
	return filepath.Join(snapDir(configPath), preRestoreName)
}

// savePreRestoreLocked は現在の設定内容を固定の復元前スロットへコピーし、
// 以前のものを上書きする。呼び出し側は mu を保持すること。
// savePreRestoreLocked copies the current config content into the fixed
// pre-restore slot, overwriting the previous one. Caller must hold mu.
func savePreRestoreLocked(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dir := snapDir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(preRestorePath(configPath), data, 0600)
}

// LastPreRestore は復元前スロットの内容を返す（存在すれば）。直近の Restore の
// 直前の設定であるため、呼び出し側は単一の「復元を元に戻す」を提供できる。
// LastPreRestore returns the content of the pre-restore slot, if present. It is
// the config as it was immediately before the most recent Restore, so a caller
// can offer a single "undo restore".
func LastPreRestore(configPath string) ([]byte, bool) {
	if configPath == "" {
		return nil, false
	}
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(preRestorePath(configPath))
	if err != nil {
		return nil, false
	}
	return data, true
}

// uniqueName は dir 内にまだ存在しないスナップショットファイル名を返す。
//
// タイムスタンプはミリ秒精度なので、同じミリ秒内の 2 回の保存（高速なマシンでは
// 十分起こり得るし、まさにテストがそうする）は同じ名前を生み、2 回目の
// WriteFileAtomic が 1 回目を上書きしてスナップショットを失ってしまう。
// 数値接尾辞が衝突を解消する。parseSnapshotTime はそれを無視する。
// uniqueName returns a snapshot file name that does not yet exist in dir.
//
// The timestamp has millisecond precision, so two saves within the same
// millisecond (very possible on a fast machine, and exactly what a test does)
// would otherwise produce the same name and the second WriteFileAtomic would
// overwrite the first, losing a snapshot. A numeric suffix disambiguates
// collisions; parseSnapshotTime ignores it.
func uniqueName(dir, base string, now time.Time) string {
	ts := now.Format(timeLayout)
	name := fmt.Sprintf("%s.%s.json", base, ts)
	if !fileExists(filepath.Join(dir, name)) {
		return name
	}
	for i := 1; i < 100000; i++ {
		cand := fmt.Sprintf("%s.%s-%d.json", base, ts, i)
		if !fileExists(filepath.Join(dir, cand)) {
			return cand
		}
	}
	// 極めて稀なフォールバック: ナノ秒精度。
	// Extremely unlikely fallback: nanosecond precision.
	return fmt.Sprintf("%s.%s.json", base, now.Format("20060102T150405.000000000"))
}

// fileExists は path が存在するか（任意の型）を返す。
// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// List はスナップショットを新しい順で返す。ディレクトリ欠落はエラーにしない。
// List returns snapshots newest-first. A missing directory is not an error.
func List(configPath string) ([]Entry, error) {
	if configPath == "" {
		return nil, nil
	}
	mu.Lock()
	defer mu.Unlock()
	snaps, err := listLocked(configPath)
	if err != nil {
		return nil, err
	}
	// listLocked は古い順で返す。UI 用に新しい順へ反転する。
	// listLocked returns oldest-first; reverse to newest-first for the UI.
	out := make([]Entry, 0, len(snaps))
	for i := len(snaps) - 1; i >= 0; i-- {
		out = append(out, snaps[i])
	}
	return out, nil
}

// listLocked はスナップショットを古い順で返す。呼び出し側は mu を保持すること。
// listLocked returns snapshots oldest-first. Caller must hold mu.
func listLocked(configPath string) ([]Entry, error) {
	dir := snapDir(configPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	base := baseName(configPath)
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ts, seq, ok := parseSnapshotTime(base, e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: e.Name(), Time: ts, Seq: seq, Size: info.Size()})
	}
	// 時刻、次に同一ミリ秒のシーケンスでソートする。時刻だけでソートすると
	// 同じミリ秒に取った 2 つのスナップショットの順序が未定義になり（Go の sort は
	// 安定でない）、高速な保存のバースト後に「最古のスナップショット」が誤った
	// ファイルになり得た。
	// Sort by time, then by the same-millisecond sequence. Sorting by time
	// alone left the order of two snapshots taken in the same millisecond
	// undefined (Go's sort is not stable), so "the oldest snapshot" could be
	// the wrong file after a fast burst of saves.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// parseSnapshotTime は "<base>.<ts>.json"（seq 0）または
// "<base>.<ts>-<n>.json"（seq n）からタイムスタンプと同一ミリ秒シーケンスを抽出する。
// parseSnapshotTime extracts the timestamp and the same-millisecond sequence
// from "<base>.<ts>.json" (seq 0) or "<base>.<ts>-<n>.json" (seq n).
func parseSnapshotTime(base, name string) (time.Time, int, bool) {
	prefix := base + "."
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
		return time.Time{}, 0, false
	}
	tsPart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
	seq := 0
	if i := strings.IndexByte(tsPart, '-'); i >= 0 {
		if n, err := strconv.Atoi(tsPart[i+1:]); err == nil {
			seq = n
		}
		tsPart = tsPart[:i]
	}
	ts, err := time.Parse(timeLayout, tsPart)
	if err != nil {
		return time.Time{}, 0, false
	}
	return ts.UTC(), seq, true
}

// Restore は configPath を指定名のスナップショットで上書きする。現在のファイルは
// 先に（ベストエフォートで）スナップショットされるため、復元自体が可逆になる。
// name はスナップショットディレクトリ内の素のファイル名でなければならない。
// Restore overwrites configPath with the named snapshot. The current file is
// snapshotted first (best effort) so a restore is itself reversible. The name
// must be a plain file name inside the snapshot directory.
func Restore(configPath, name string) error {
	if configPath == "" {
		return fmt.Errorf("設定パスが空です")
	}
	clean := filepath.Base(name)
	if clean != name || clean == "." || clean == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("不正なスナップショット名です: %s", name)
	}
	mu.Lock()
	defer mu.Unlock()

	src := filepath.Join(snapDir(configPath), clean)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// 直近の復元前状態だけを、上書きされる単一スロットに保つ。復元は設定変更では
	// ないため、タイムスタンプ付きスナップショットを追加してはならない。
	// そうすると復元のたびに履歴が増え、運用者の本当の保存を押し出してしまう。
	// 最後の復元前状態だけが有用で（直前に戻した復元を元に戻せる）、List() が
	// 無視する名前で保存する。
	// Keep only the most recent pre-restore state in a single, overwritten
	// slot. A restore is not a config change, so it must NOT add a timestamped
	// snapshot: doing so made every restore grow the history and push out the
	// operator's real saves. Only the last pre-restore state is useful (it lets
	// the immediately previous restore be undone), and it is stored under a
	// name that List() ignores.
	_ = savePreRestoreLocked(configPath)
	mode := fsutil.SharedFileMode(configPath, 0600)
	return fsutil.WriteFileAtomic(configPath, data, mode)
}

// pruneLocked は GFS ポリシー外のスナップショットを除去する。呼び出し側は mu を
// 保持すること。
// pruneLocked removes snapshots that fall outside the GFS policy. Caller must
// hold mu.
func pruneLocked(configPath string, now time.Time) error {
	snaps, err := listLocked(configPath)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return nil
	}
	keep := make(map[string]bool, len(snaps))

	// 1) 常に最新の keepRecent 件を保持する。
	// 1) Always keep the newest keepRecent snapshots.
	kept := 0
	for i := len(snaps) - 1; i >= 0 && kept < keepRecent; i-- {
		keep[snaps[i].Name] = true
		kept++
	}

	// 2) GFS バケット。新しい順に歩き、日/週/月ごとに最初に見たものを保つ。
	// 2) GFS buckets, walking newest-first: keep the first seen per day/week/month.
	seenDay := map[string]bool{}
	seenWeek := map[string]bool{}
	seenMonth := map[string]bool{}
	dailyCut := now.AddDate(0, 0, -keepDaily)
	weeklyCut := now.AddDate(0, 0, -7*keepWeekly)
	monthlyCut := now.AddDate(0, 0, -30*keepMonthly)
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		switch {
		case s.Time.After(dailyCut):
			d := s.Time.Format("2006-01-02")
			if !seenDay[d] {
				seenDay[d] = true
				keep[s.Name] = true
			}
		case s.Time.After(weeklyCut):
			y, w := s.Time.ISOWeek()
			k := fmt.Sprintf("%d-W%02d", y, w)
			if !seenWeek[k] {
				seenWeek[k] = true
				keep[s.Name] = true
			}
		case s.Time.After(monthlyCut):
			m := s.Time.Format("2006-01")
			if !seenMonth[m] {
				seenMonth[m] = true
				keep[s.Name] = true
			}
		default:
			// 月次窓より古い: 削除する。
			// Older than the monthly window: drop.
		}
	}

	for _, s := range snaps {
		if !keep[s.Name] {
			_ = os.Remove(filepath.Join(snapDir(configPath), s.Name))
		}
	}
	return nil
}
