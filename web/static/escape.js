// ============================================================
// Kizuna-Eye - 共有エスケープユーティリティ
// app.js / modules.js に重複定義されていた escapeHtml / escapeAttr を一元化する。
// プラグイン（.so / meta.json）やログ由来の文字列を innerHTML へ埋め込む前に
// 必ず escapeHtml（テキスト）／ escapeAttr（属性値）を通すこと。
// ============================================================
(function (global) {
    'use strict';

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
    function escapeAttr(value) {
        return escapeHtml(value);
    }

    global.escapeHtml = escapeHtml;
    global.escapeAttr = escapeAttr;
    global.KizunaEscape = { html: escapeHtml, attr: escapeAttr };

    // node からの単体テスト用（ブラウザでは module が未定義なので実行されない）。
    if (typeof module !== 'undefined' && module.exports) {
        module.exports = { escapeHtml: escapeHtml, escapeAttr: escapeAttr };
    }
})(typeof window !== 'undefined' ? window : globalThis);
