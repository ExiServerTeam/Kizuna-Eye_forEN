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

    function getTheme() {
        return document.documentElement.getAttribute('data-theme') || 'dark';
    }

    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        var icon = document.getElementById('authThemeIcon') || document.getElementById('themeIcon');
        if (icon) icon.textContent = theme === 'dark' ? '🌙' : '☀️';
        var label = theme === 'dark' ? 'ライトモードに切り替え' : 'ダークモードに切り替え';
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
        var buttons = [
            document.getElementById('authThemeToggle'),
            document.getElementById('themeToggle')
        ];
        var any = false;
        buttons.forEach(function (btn) {
            if (!btn || btn.dataset.themeWired === '1') return;
            btn.dataset.themeWired = '1';
            any = true;
            btn.addEventListener('click', function () {
                setTheme(getTheme() === 'dark' ? 'light' : 'dark');
            });
        });
        if (any) applyTheme(getTheme());

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
})();
