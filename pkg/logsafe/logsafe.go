// logsafe パッケージは、信頼できないテキストをログ行に書く前に無害化する。
// これは 2 つのドリフトしたコピーから抽出した。auth ハンドラ版は制御文字を除去し
// 角括弧をエスケープしていたが、dashboard 版はタブ・改行を空白に置換し角括弧を
// 放置していた。そのため細工したプラグインイベントが dashboard.log に "[WARN]"
// マーカーを偽造できた（監査所見 L-11）。
// Package logsafe neutralises untrusted text before it is written to a log
// line. It was extracted from two copies that had drifted apart: the auth
// handler version dropped control characters and escaped brackets, while the
// dashboard copy replaced tabs/newlines with a space and left brackets alone,
// so a crafted plugin event could still forge a "[WARN]" marker in
// dashboard.log (audit finding L-11).
package logsafe

import "strings"

// Field は信頼できないテキストをログ用に無害化する。
//
// 2 種類のインジェクションを処理する。
//
//  1. 改行・制御文字。CR・LF・タブその他の C0/C1 制御文字（NUL・ESC・ANSI 導入子など）は
//     空白に置換するのではなく「除去」する。空白に置換すると "user=a [INFO] FORGED from=..."
//     のような読める偽造断片が残り、ログパーサが実エントリと誤認し得る。
//
//  2. ログパーサのメタ文字。角括弧はパーサ（および人間）がレベルタグ（[INFO]、[WARN] など）を
//     見分ける目印だ。バックスラッシュでエスケープし、注入された "[INFO] FORGED" が
//     実ログレベルに見えないようにする。
//
// この値はログ専用だ。認証やマッチングは生の入力を使い続けること。
// Field neutralises untrusted text for logging.
//
// Two classes of injection are handled:
//
//  1. Line breaking / control characters. CR, LF, tab and other C0/C1 controls
//     (NUL, ESC, ANSI introducers, ...) are DROPPED, not replaced with a space.
//     Replacing them with a space still leaves a readable forged fragment such
//     as "user=a [INFO] FORGED from=...", which a log parser can mistake for a
//     real entry.
//
//  2. Log-parser metacharacters. Brackets are the marker a parser (and a human)
//     uses to spot a level tag ([INFO], [WARN], ...). They are escaped with a
//     backslash so an injected "[INFO] FORGED" can never look like a real log
//     level.
//
// The value is only used for logging; authentication and matching must keep
// using the raw input.
func Field(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			// 文字を完全に除去する。空白だと偽造 "[INFO]" タグが周囲のテキストから
			// 分離されたまま残ってしまう。
			// Drop the character entirely: a space would still separate a
			// forged "[INFO]" tag from the surrounding text.
			continue
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// その他の制御文字（NUL・ESC・ANSI 導入子など）を除去する。
			// Drop other control characters (NUL, ESC, ANSI introducers, ...).
			continue
		case r == '[' || r == ']':
			// 角括弧をエスケープし、注入された "[INFO]"/"[WARN]" タグが
			// パーサや人間に実ログレベルとして読まれないようにする。
			// Escape the bracket so an injected "[INFO]"/"[WARN]" tag cannot
			// be read as a real log level by a parser or by a human.
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
