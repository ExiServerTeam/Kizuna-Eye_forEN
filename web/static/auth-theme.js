// ============================================================
// Kizuna-Eye - 共通テーマ制御（認証ページ / ユーザー管理ページ用）
// - ダッシュボードと同じ localStorage キー (kizuna-theme) を使う
// - OS 設定 (prefers-color-scheme) にも追従
// - CSP 対策のため外部ファイル。インラインスクリプトは使わない。
// - #authThemeToggle / #authThemeIcon と #themeToggle / #themeIcon の
//   両方に対応する。
// ============================================================
(function () {
    'use strict';

    var STORAGE_KEY = 'kizuna-theme';

    // t() uses the shared i18n dictionary when present, falling back to the
    // key itself (or a default) so this file also works before i18n.js loads.
    function t(key, fallback) {
        if (window.KizunaI18n && window.KizunaI18n.t) {
            var v = window.KizunaI18n.t(key);
            if (v && v !== key) return v;
        }
        return fallback;
    }

    function getTheme() {
        return document.documentElement.getAttribute('data-theme') || 'dark';
    }

    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        var icon = document.getElementById('authThemeIcon') || document.getElementById('themeIcon');
        if (icon) icon.textContent = theme === 'dark' ? '🌙' : '☀️';
        var label = theme === 'dark'
            ? t('theme.toggle_to_light', 'Switch to light mode')
            : t('theme.toggle_to_dark', 'Switch to dark mode');
        var buttons = [
            document.getElementById('authThemeToggle'),
            document.getElementById('themeToggle')
        ];
        buttons.forEach(function (btn) {
            if (!btn) return;
            btn.setAttribute('aria-label', label);
            btn.setAttribute('title', label);
        });
    }

    function setTheme(theme) {
        applyTheme(theme);
        try { localStorage.setItem(STORAGE_KEY, theme); } catch (e) { /* ignore */ }
    }

    // Apply as early as possible so the page does not flash the wrong theme.
    var saved = null;
    try { saved = localStorage.getItem(STORAGE_KEY); } catch (e) { /* ignore */ }
    if (saved === 'dark' || saved === 'light') {
        applyTheme(saved);
    } else if (window.matchMedia) {
        applyTheme(window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    }

    function wire() {
        // 注意: ダッシュボードの #themeToggle は app.js が click を処理する。
        // ここでも click を足すと 1 クリックで 2 回トグルし、見た目上「切り替え
        // が死んで」しまう（ダーク→ライト→ダーク）。したがって auth-theme.js
        // が click を張るのは認証ページ専用の #authThemeToggle だけにする。
        // #themeToggle にはアイコン/ラベルの反映（applyTheme）だけ行う。
        var authBtn = document.getElementById('authThemeToggle');
        var any = false;
        if (authBtn && authBtn.dataset.themeWired !== '1') {
            authBtn.dataset.themeWired = '1';
            any = true;
            authBtn.addEventListener('click', function () {
                setTheme(getTheme() === 'dark' ? 'light' : 'dark');
            });
        }
        // アイコン/ラベルを現在のテーマに合わせる（click は張らない）。
        applyTheme(getTheme());
        void any;

        // Follow OS changes only when the user has not chosen manually.
        if (window.matchMedia) {
            var mq = window.matchMedia('(prefers-color-scheme: dark)');
            var onChange = function (e) {
                var manual = null;
                try { manual = localStorage.getItem(STORAGE_KEY); } catch (err) { /* ignore */ }
                if (!manual) applyTheme(e.matches ? 'dark' : 'light');
            };
            if (mq.addEventListener) mq.addEventListener('change', onChange);
            else if (mq.addListener) mq.addListener(onChange);
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', wire);
    } else {
        wire();
    }

    // Re-apply the theme label when the language changes (the aria-label /
    // title are not data-i18n because they are computed).
    window.addEventListener('kizuna-lang-change', function () { applyTheme(getTheme()); });
})();
