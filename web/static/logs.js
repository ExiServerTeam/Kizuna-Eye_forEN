// ============================================================
// Kizuna-Eye - ログビューア
// ============================================================
(function () {
    'use strict';

    const logViewer = document.getElementById('logViewer');
    const logStatus = document.getElementById('logStatus');
    const logLines = document.getElementById('logLines');
    const reloadBtn = document.getElementById('logReloadBtn');
    const followBtn = document.getElementById('logFollowBtn');
    const typeAgentBtn = document.getElementById('logTypeAgent');
    const typeDashboardBtn = document.getElementById('logTypeDashboard');

    // i18n 翻訳ヘルパー
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    let currentType = 'agent';
    let follow = false;
    let followTimer = null;

    // テーマ
    const themeToggle = document.getElementById('themeToggle');
    const themeIcon = document.getElementById('themeIcon');
    const themeLabel = document.getElementById('themeLabel');

    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        localStorage.setItem('kizuna-theme', theme);
        if (themeIcon) themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
    }

    const savedTheme = localStorage.getItem('kizuna-theme');
    if (savedTheme) {
        applyTheme(savedTheme);
    } else {
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        applyTheme(prefersDark ? 'dark' : 'light');
    }

    if (themeToggle) {
        themeToggle.addEventListener('click', () => {
            const current = document.documentElement.getAttribute('data-theme') || 'dark';
            applyTheme(current === 'dark' ? 'light' : 'dark');
        });
    }

    function setType(type) {
        currentType = type;
        if (typeAgentBtn) typeAgentBtn.classList.toggle('active', type === 'agent');
        if (typeDashboardBtn) typeDashboardBtn.classList.toggle('active', type === 'dashboard');
        loadLogs();
    }

    async function loadLogs() {
        const lines = logLines ? logLines.value : '100';
        if (logStatus) logStatus.textContent = t('logs.loading');
        try {
            const res = await fetch(`/api/logs?type=${encodeURIComponent(currentType)}&lines=${encodeURIComponent(lines)}`);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const text = await res.text();
            if (logViewer) {
                logViewer.textContent = text || t('logs.no_logs');
                // 末尾へスクロール
                logViewer.scrollTop = logViewer.scrollHeight;
            }
            if (logStatus) {
                logStatus.textContent = `${currentType}: ${lines} / ${new Date().toLocaleTimeString()}`;
            }
        } catch (e) {
            if (logViewer) logViewer.textContent = `${t('status.error')}: ${e.message}`;
            if (logStatus) logStatus.textContent = t('status.error');
        }
    }

    function toggleFollow() {
        follow = !follow;
        if (followBtn) {
            followBtn.textContent = follow ? t('logs.follow_on') : t('logs.follow_off');
            followBtn.classList.toggle('active', follow);
        }
        if (follow) {
            followTimer = setInterval(loadLogs, 3000);
        } else if (followTimer) {
            clearInterval(followTimer);
            followTimer = null;
        }
    }

    if (reloadBtn) reloadBtn.addEventListener('click', loadLogs);
    if (followBtn) followBtn.addEventListener('click', toggleFollow);
    if (typeAgentBtn) typeAgentBtn.addEventListener('click', () => setType('agent'));
    if (typeDashboardBtn) typeDashboardBtn.addEventListener('click', () => setType('dashboard'));
    if (logLines) logLines.addEventListener('change', loadLogs);

    window.addEventListener('kizuna-lang-change', () => {
        if (followBtn) followBtn.textContent = follow ? t('logs.follow_on') : t('logs.follow_off');
        loadLogs();
    });

    loadLogs();
})();
