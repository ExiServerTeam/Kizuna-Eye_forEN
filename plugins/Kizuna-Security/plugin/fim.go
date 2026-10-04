package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

// fimUnreadableWarnInterval is how often an unreadable watch target is
// re-reported. 非 root 運用では /etc/shadow などが常に読めないため、短い
// 間隔で通知すると WARN でログと通知が埋まり本物の異常が見えなくなる。
// それでも監視不能を放置しないよう、既定は24時間ごとに1回だけ再通知する。
var fimUnreadableWarnInterval = 24 * time.Hour

// FIM (File Integrity Monitoring) は重要ファイルの改ざん・作成・削除を検知する。
//
// 初回スキャンでベースライン（各ファイルの SHA-256）を記録し、以降の
// スキャンで差分を検知してイベントを発行する。ベースラインはファイルに
// 永続化し、再起動で「全ファイルが新規」と誤検知しないようにする。
type FIM struct {
	files        []string
	baselinePath string
	logger       module.Logger
	emitFn       func(module.SecurityEvent)

	mu          sync.Mutex
	baseline    map[string]string // path -> sha256
	watched     map[string]bool   // 監視対象として既知のパス（新規追加の誤警報防止）
	initialized bool              // ベースライン取得済みか（空baselineと未取得を区別）
	language    string            // 通知メッセージの言語

	// key はベースライン署名用の鍵。空なら鍵なし SHA-256（F-4）。
	key []byte
	// pendingBaseline は「署名付きベースラインを鍵なしで読んだため、改ざんと
	// 断定できない」保留状態（F-4）。鍵付き運用の再起動で誤報を出さないよう
	// 通知を保留し、判定は SetChainKey（鍵が来た）または Check（来なかった）
	// で確定する。
	pendingBaseline bool
	// unreadable は読み取り不能を通知済みのパス（通知の重複防止、F-4）。
	unreadable map[string]bool
	// unreadableAt は読み取り不能を最後に通知した時刻。再起動しても毎回
	// 通知しないよう、ベースラインと同じファイルへ永続化する。
	unreadableAt map[string]time.Time

	// langMu guards language only. It must be separate from mu: Check()
	// holds mu while emitting events that read the language, so sharing mu
	// would make lang() re-lock a mutex Check() already holds (Go mutexes
	// are not reentrant) and deadlock the plugin's run loop.
	langMu sync.RWMutex
}

// fimState はベースラインの永続化形式。
type fimState struct {
	Baseline map[string]string `json:"baseline"`
	Watched  []string          `json:"watched"`
	// Unreadable maps a path that could not be read to the Unix time of the
	// last notification. 再起動のたびに同じ「読めません」警告を繰り返さない
	// ために保存する（非 root 運用では常時発生するため）。omitempty なので
	// このフィールドを追加しても既存ベースラインの署名は変わらない。
	Unreadable map[string]int64 `json:"unreadable,omitempty"`
	// Sig はベースライン自身の署名（F-4）。これが無いと、ベースライン
	// ファイルを書き換えて「改ざん無し」の状態を作れてしまう。
	Sig string `json:"sig,omitempty"`
}

// signFimState は Sig を除いた状態の署名を返す（規則は signJSON と同じ）。
func signFimState(key []byte, st fimState) string {
	st.Sig = ""
	return signJSON(key, st)
}

// signJSON は v を JSON へ直列化して署名する。json.Marshal はキーを
// ソートするため正規形は決定的である。鍵があれば HMAC-SHA256、無ければ
// SHA-256（チェーンの chainHashKeyed と同じ規則）。
//
// FIM 本体のベースラインとディレクトリ監視のベースラインが同じ規則で署名
// するために共通化している。呼び出し側は Sig フィールドを空にしてから渡す
// こと（自分自身の署名を対象に含めない）。
func signJSON(key []byte, v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(key) > 0 {
		h := hmac.New(sha256.New, key)
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func NewFIM(files []string, baselinePath string, logger module.Logger, emitFn func(module.SecurityEvent)) *FIM {
	return NewFIMKeyed(files, baselinePath, logger, emitFn, nil)
}

// NewFIMKeyed は署名鍵を最初から設定した状態でベースラインを読み込む（F-4）。
//
// NewFIM で読んでから SetChainKey すると、鍵を知らないうちに署名付き
// ベースラインを読み、「鍵が無いので署名が一致しない」＝改ざんと誤判定して
// critical を通知してしまう（鍵付き運用では再起動のたびに誤報が出る）。
// 鍵を先に渡せば、最初の読み込みから正しい鍵で検証できる。
func NewFIMKeyed(files []string, baselinePath string, logger module.Logger, emitFn func(module.SecurityEvent), key []byte) *FIM {
	f := &FIM{
		files:        files,
		baselinePath: baselinePath,
		logger:       logger,
		emitFn:       emitFn,
		baseline:     make(map[string]string),
		watched:      make(map[string]bool),
		unreadable:   make(map[string]bool),
		unreadableAt: make(map[string]time.Time),
		key:          key,
	}
	f.initialized = f.loadBaseline()
	return f
}

// SetChainKey はベースライン署名に使う鍵を設定する（F-4）。
// 最初の Check より前に呼ぶこと。
func (f *FIM) SetChainKey(key []byte) {
	if f == nil {
		return
	}
	f.key = key
	// コンストラクタは鍵より先にベースラインを読むため、鍵を受け取った
	// 時点で署名を検証し直す（署名の無いベースラインをここで拒否する）。
	// 保留していた「鍵が無いので判定できない」状態もここで確定する（F-4）。
	f.pendingBaseline = false
	f.initialized = f.loadBaseline()
}

// Check compares the current hashes against the baseline and emits events.
// The first run records the baseline without emitting events.
func (f *FIM) Check() {
	if f == nil || len(f.files) == 0 {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// 鍵が設定されないまま Check が来た場合、保留していた署名検証の判定を
	// ここで確定する（鍵なし運用でベースラインが改ざんされていた場合は
	// critical 通知。鍵付き運用では SetChainKey が先に確定させるのでここには
	// 入らない）。F-4。
	if f.pendingBaseline {
		f.pendingBaseline = false
		f.warnBaselineTamper("鍵が設定されていないため署名を照合できません（改ざんの可能性）")
	}

	current := make(map[string]string, len(f.files))
	unreadable := make(map[string]bool)
	unreadErr := make(map[string]string)
	unreadPerm := make(map[string]bool)
	for _, p := range f.files {
		h, err := hashFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				// 存在しないファイルは従来どおり「削除」検知に任せる。
				// 未作成の監視対象を毎回警告しないため（F-1 の回帰テスト
				// TestFIMDetectsCreateOfFileMissingAtBaseline も参照）。
				continue
			}
			// 権限不足や symlink 差し替えなどで読めない場合は、改ざんを
			// 検知できない状態なので必ず通知し、ベースラインからも消さない
			// （F-4: chmod 000 で改ざんを永久に見逃す抜け道を塞ぐ）。
			// 既知（ベースライン登録済み）のファイルだけを対象にする。
			if f.baseline[p] != "" || f.watched[p] {
				unreadable[p] = true
				unreadErr[p] = err.Error()
				// 権限不足（非 root 運用で常時発生）とその他の原因を区別する。
				unreadPerm[p] = os.IsPermission(err)
			}
			continue
		}
		current[p] = h
		// 読める状態に戻ったら通知履歴を消す（次に読めなくなった時に
		// 間隔を待たず即通知する）。
		delete(f.unreadableAt, p)
	}

	// F-4: 読み取り不能は「改ざんを検知できない状態」なので黙って落とさない。
	// ただし権限不足は非 root 運用では常時発生し（/etc/shadow など）、warning の
	// まま毎回通知するとログと Discord が埋まり本物の異常が見えなくなる。そこで
	//   - 権限不足 → info（履歴には残る。他の原因は warning のまま）
	//   - 通知は「状態が変化した時」＋「前回通知から
	//     fimUnreadableWarnInterval（既定24時間）経過時」だけ
	//   - 最終通知時刻はベースラインへ永続化し、再起動でも繰り返さない
	// という扱いにする。
	if f.unreadableAt == nil {
		f.unreadableAt = make(map[string]time.Time)
	}
	var pendingPerm, pendingOther []string
	for p := range unreadable {
		// 通知済みで、まだ再通知の間隔内なら見送る。「読めなかった」事実は
		// ディスクに残るため、再起動直後でも同じ警告を繰り返さない
		// （読める状態に戻れば下の走査で履歴が消え、再び読めなくなった時に
		// 即通知される）。
		if last, notified := f.unreadableAt[p]; notified && time.Since(last) < fimUnreadableWarnInterval {
			continue
		}
		// 権限不足は非 root 運用では常時発生するため info へ降格する。
		// symlink 差し替えなど権限以外の理由は warning のまま（危険度が違う）。
		if unreadPerm[p] {
			pendingPerm = append(pendingPerm, p)
		} else {
			pendingOther = append(pendingOther, p)
		}
		f.unreadableAt[p] = time.Now()
	}

	// 同じチェックで複数件あっても通知は1件にまとめる。監視対象が多い環境で
	// 1ファイル1通知にすると、ログと通知が同じ内容で埋まり本物の異常が
	// 見えなくなる（1件のときは従来どおり個別のメッセージを出す）。
	pending := append(append([]string{}, pendingPerm...), pendingOther...)
	sort.Strings(pending)
	if len(pending) > 0 {
		level := "info"
		if len(pendingOther) > 0 {
			level = "warning"
		}
		if f.emitFn != nil {
			ev := module.SecurityEvent{
				Category:  "integrity",
				Level:     level,
				Title:     msg(f.lang(), "integrity.unreadable.title"),
				Source:    strings.Join(pending, ","),
				Timestamp: time.Now(),
			}
			if len(pending) == 1 {
				key := "integrity.unreadable.perm.msg"
				if len(pendingOther) == 1 {
					key = "integrity.unreadable.msg"
				}
				ev.Source = pending[0]
				ev.Message = msg(f.lang(), key, pending[0], unreadErr[pending[0]])
			} else {
				key := "integrity.unreadable.multi.perm.msg"
				if len(pendingOther) > 0 {
					key = "integrity.unreadable.multi.msg"
				}
				ev.Message = msg(f.lang(), key, len(pending), strings.Join(pending, ", "))
			}
			f.emitFn(ev)
		}
	}
	// 読めるように戻ったら履歴を消し、次に読めなくなった時点で再通知する。
	for p := range f.unreadable {
		if !unreadable[p] {
			delete(f.unreadableAt, p)
		}
	}
	f.unreadable = unreadable

	// 初回（ベースライン未取得）はイベントを出さずに記録する。
	if !f.initialized {
		f.initialized = true
		f.baseline = current
		for p := range current {
			f.watched[p] = true
		}
		f.saveBaselineLocked()
		if f.logger != nil {
			f.logger.Info("Kizuna-Security FIM: ベースラインを記録しました (%d ファイル)", len(current))
		}
		return
	}

	now := time.Now()

	// 現在存在するファイルの作成・変更を検知。
	for p, h := range current {
		old, existed := f.baseline[p]
		if !existed {
			if !f.watched[p] {
				// 監視対象に新しく追加されたファイルは、黙って取り込まず
				// 必ず通知する。管理者が意図した追加なら確認できるようにし、
				// 攻撃者が新しいファイルをベースラインへ紛れ込ませる抜け道を
				// 塞ぐ（気付けるようにする）。
				f.emitFn(module.SecurityEvent{
					Category:  "integrity",
					Level:     "warning",
					Title:     msg(f.lang(), "integrity.baseline.title"),
					Message:   msg(f.lang(), "integrity.baseline.msg", p),
					Source:    p,
					Timestamp: now,
				})
				continue
			}
			f.emitFn(module.SecurityEvent{
				Category:  "integrity",
				Level:     "critical",
				Title:     msg(f.lang(), "integrity.create.title"),
				Message:   msg(f.lang(), "integrity.create.msg", p),
				Source:    p,
				Timestamp: now,
			})
			continue
		}
		if old != h {
			f.emitFn(module.SecurityEvent{
				Category:  "integrity",
				Level:     "critical",
				Title:     msg(f.lang(), "integrity.change.title"),
				Message:   msg(f.lang(), "integrity.change.msg", p),
				Source:    p,
				Timestamp: now,
			})
		}
	}

	// 削除を検知（監視対象として既知のファイルのみ）。
	for p := range f.baseline {
		if !f.watched[p] {
			continue
		}
		if unreadable[p] {
			// 読み取り不能は「削除」ではなく上で警告済み（F-4）。
			continue
		}
		if _, ok := current[p]; !ok {
			f.emitFn(module.SecurityEvent{
				Category:  "integrity",
				Level:     "warning",
				Title:     msg(f.lang(), "integrity.delete.title"),
				Message:   msg(f.lang(), "integrity.delete.msg", p),
				Source:    p,
				Timestamp: now,
			})
		}
	}

	// 読み取り不能なファイルは「消えた」と誤判定しないよう、直前の
	// ベースライン値を保持する（F-4）。
	for p := range unreadable {
		if old, ok := f.baseline[p]; ok {
			current[p] = old
		}
	}
	f.baseline = current
	for p := range current {
		f.watched[p] = true
	}
	f.saveBaselineLocked()
}

// lang is the notification language. FIM has no config pointer, so the
// language is captured from the package default; it is injected via SetLang.
func (f *FIM) lang() string {
	f.langMu.RLock()
	defer f.langMu.RUnlock()
	if f.language == "" {
		return "ja"
	}
	return f.language
}

// SetLang sets the language used for FIM notifications.
func (f *FIM) SetLang(lang string) {
	f.langMu.Lock()
	f.language = lang
	f.langMu.Unlock()
}

// hashFile returns the hex SHA-256 of a regular file.
func hashFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", os.ErrInvalid
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadBaseline loads the saved baseline. It returns true when a baseline
// file existed and was parsed (even if it contained zero entries).
// 旧形式（path->hash のフラットな map）も後方互換で読み込む。
func (f *FIM) loadBaseline() bool {
	data, err := os.ReadFile(f.baselinePath)
	if err != nil {
		return false
	}

	var st fimState
	if json.Unmarshal(data, &st) == nil && st.Baseline != nil {
		if !f.verifyBaselineSignature(st) {
			// 署名を検証できないベースラインは信用せず、取り直す。
			// （改ざんは critical 通知済み。ここで false を返すと
			//  未取得扱いになり再ベースラインされる）
			return false
		}
		f.baseline = st.Baseline
		for _, p := range st.Watched {
			f.watched[p] = true
		}
		// 読取不能の通知履歴も復元し、再起動で同じ警告を繰り返さない。
		for p, ts := range st.Unreadable {
			if ts > 0 {
				f.unreadableAt[p] = time.Unix(ts, 0)
			}
		}
		return true
	}

	var m map[string]string
	if json.Unmarshal(data, &m) == nil {
		// 旧形式（未署名の path->hash）。鍵を設定している運用では、
		// 署名を削るだけで検証を回避できてしまうため受け入れない（F-4）。
		if len(f.key) > 0 {
			f.warnBaselineTamper("署名の無いベースライン（旧形式）を検出")
			return false
		}
		f.baseline = m
		for p := range m {
			f.watched[p] = true
		}
		return true
	}
	return false
}

// verifyBaselineSignature はベースラインの署名を検証する。署名が無い場合は
// 鍵が設定されていれば拒否し（署名削除による検証回避を防ぐ）、鍵が無ければ
// 受け入れる。不一致は critical 通知のうえ拒否する（F-4）。
//
// 鍵が未設定のまま署名付きベースラインを読んだ場合は、鍵なし SHA-256 として
// 照合する。一致すれば鍵なし運用で改ざんは無い。一致しなければ「HMAC 署名済み
// で鍵が後から設定される（NewFIMKeyed / SetChainKey）」か「改ざん」かの
// どちらかで、この時点では確定できない。ここで通知すると鍵付き運用の再起動
// ごとに誤報が出るため、pendingBaseline を立てて通知を保留し、SetChainKey
// または Check で判定する。
func (f *FIM) verifyBaselineSignature(st fimState) bool {
	if st.Sig == "" {
		if len(f.key) > 0 {
			f.warnBaselineTamper("署名がありません")
			return false
		}
		return true
	}
	if len(f.key) == 0 {
		if signFimState(nil, st) == st.Sig {
			return true
		}
		f.pendingBaseline = true
		return false
	}
	want := signFimState(f.key, st)
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(st.Sig)) != 1 {
		f.warnBaselineTamper("署名が一致しません")
		return false
	}
	return true
}

// warnBaselineTamper はベースライン改ざん（署名不一致）を通知する（F-4）。
func (f *FIM) warnBaselineTamper(reason string) {
	if f.emitFn != nil {
		f.emitFn(module.SecurityEvent{
			Category:  "integrity",
			Level:     "critical",
			Title:     msg(f.lang(), "integrity.baseline.tamper.title"),
			Message:   msg(f.lang(), "integrity.baseline.tamper.msg", f.baselinePath, reason),
			Source:    f.baselinePath,
			Timestamp: time.Now(),
		})
	}
	if f.logger != nil {
		f.logger.Error("Kizuna-Security FIM: ベースライン署名の検証に失敗: %s", reason)
	}
}

// saveBaselineLocked writes the baseline atomically (m.mu must be held).
func (f *FIM) saveBaselineLocked() {
	for _, p := range f.files {
		f.watched[p] = true
	}
	watched := make([]string, 0, len(f.watched))
	for p := range f.watched {
		watched = append(watched, p)
	}
	st := fimState{Baseline: f.baseline, Watched: watched}
	// 読取不能の最終通知時刻も保存する（再起動で警告を繰り返さないため）。
	if len(f.unreadableAt) > 0 {
		st.Unreadable = make(map[string]int64, len(f.unreadableAt))
		for p, ts := range f.unreadableAt {
			if !ts.IsZero() {
				st.Unreadable[p] = ts.Unix()
			}
		}
	}
	// F-4: ベースライン自身を署名して、ファイルを書き換えるだけで
	// 「改ざん無し」の状態を作れないようにする。
	st.Sig = signFimState(f.key, st)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(f.baselinePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	// fsutil: 同一ディレクトリへ一時ファイル→fsync→rename。従来は fsync が
	// 無く、クラッシュで空のベースラインが残ると「全ファイルが新規」に
	// 見える（＝改ざんが消える）恐れがあった（L-12）。
	_ = fsutil.WriteFileAtomic(f.baselinePath, data, 0600)
}
