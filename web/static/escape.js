// ============================================================
// Kizuna-Eye - 共有エスケープユーティリティ
// app.js / modules.js に重複定義されていた escapeHtml / escapeAttr を一元化する。
// プラグイン（.so / meta.json）やログ由来の文字列を innerHTML へ埋め込む前に
// 必ず escapeHtml（テキスト）／ escapeAttr（属性値）を通すこと。
// Kizuna-Eye - shared escape utilities
// Centralises the escapeHtml / escapeAttr that were duplicated in app.js and
// modules.js. Always pass plugin (.so / meta.json) or log-derived strings
// through escapeHtml (text) / escapeAttr (attribute value) before embedding
// them in innerHTML.
// ============================================================
(function (global) {
    'use strict';

    // escapeHtml は & < > " ' をエスケープし、結果がテキスト内容としても、
    // 二重引用符・単一引用符で囲まれた属性値の中でも安全になるようにする。
    // escapeHtml escapes & < > " ' so the result is safe both as text content
    // and inside a double-quoted/single-quoted attribute value.
    function escapeHtml(value) {
        return String(value == null ? '' : value)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    }

    // 属性値用の別名。実装は同一だが、呼び出し側の意図を明示するために分けている。
    // Alias for attribute values. The implementation is identical, but it is
    // split out to make the caller's intent explicit.
    function escapeAttr(value) {
        return escapeHtml(value);
    }

    // CSS クラス名用。データ（プラグインmeta・エージェント応答・ログ）から
    // クラス名を組み立てる時は必ずこれを通す。
    //   - 引用符・山括弧・スラッシュ等は除去されるため class="..." から抜け出せない
    //   - クラス名として使える文字（英数字・_・-・空白）以外は落ちるため、
    //     複数クラスの偽装（"card admin" のような注入）もできない
    // 文字列以外（数値・真偽値）は String() で正規化し、null/undefined は '' にする。
    // For CSS class names. Always pass through this when building a class name
    // from data (plugin meta, agent responses, logs).
    //   - quotes, angle brackets, slashes, etc. are removed, so it cannot
    //     break out of class="..."
    //   - characters that are not usable in a class name (alphanumerics, _, -,
    //     space) are dropped, so forging multiple classes (an injection like
    //     "card admin") is not possible either.
    // Non-strings (numbers, booleans) are normalised via String(); null and
    // undefined become ''.
    function cssClass(value) {
        return String(value == null ? '' : value)
            .replace(/[^\w\- ]+/g, '')
            .trim();
    }

    global.escapeHtml = escapeHtml;
    global.escapeAttr = escapeAttr;
    global.cssClass = cssClass;
    global.KizunaEscape = { html: escapeHtml, attr: escapeAttr, cls: cssClass };

    // node からの単体テスト用（ブラウザでは module が未定義なので実行されない）。
    // For unit tests run from node (not executed in the browser, where module
    // is undefined).
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = { escapeHtml: escapeHtml, escapeAttr: escapeAttr, cssClass: cssClass };
    }
})(typeof window !== 'undefined' ? window : globalThis);
