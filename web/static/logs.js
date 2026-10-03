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
    const typeButtonsEl = document.getElementById('logTypeButtons');
    const formatBtn = document.getElementById('logFormatBtn');

    // i18n 翻訳ヘルパー
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    // 表示は最大 500 行。超えたら古い行から捨てる。
    const MAX_VIEW_LINES = 500;
    const FOLLOW_KEY = 'kizuna-logs-follow';
    const FORMAT_KEY = 'kizuna-logs-format';

    // 自動更新は既定で ON。OFF にした場合は localStorage に記憶し、
    // 再読み込みしても設定が既定（ON）へ戻らないようにする。
    let follow = true;
    try {
        const saved = localStorage.getItem(FOLLOW_KEY);
        if (saved === 'off') follow = false;
        else if (saved === 'on') follow = true;
    } catch (e) { /* ignore */ }

    // 表示形式: 既定は整形（人間向け）。JSON の生データにも切り替えられる。
    let formatMode = 'pretty';
    try {
        const saved = localStorage.getItem(FORMAT_KEY);
        if (saved === 'raw') formatMode = 'raw';
        else if (saved === 'pretty') formatMode = 'pretty';
    } catch (e) { /* ignore */ }

    let currentType = 'agent';
    let followTimer = null;

    // 動的に追加したプラグインログのボタンを保持する
    const dynamicTypeButtons = new Map(); // type id -> button element

    // ============================================================
    // ログの整形（JSON Lines を人間向けに変換）
    // ============================================================
    // プラグインのログは 1 行 1 JSON の JSON Lines 形式。機械処理には向くが
    // 人間には読みにくいので、既定では「時刻 [レベル] メッセージ (補足)」の
    // 形に整形して表示する。JSON でない行（Agent/Dashboard のテキストログ）
    // はそのまま出す。
    const SKIP_KEYS = new Set(['hash', 'prev_hash', 'ts', 'level', 'message', 'event', 'icon']);

    function pad2(n) { return String(n).padStart(2, '0'); }

    function formatTimestamp(ts) {
        if (!ts) return '';
        const d = new Date(ts);
        if (isNaN(d.getTime())) return String(ts);
        return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ` +
               `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
    }

    function formatJSONLine(line) {
        const trimmed = line.trim();
        if (!trimmed || trimmed[0] !== '{') return line;   // JSON でない -> そのまま
        let obj;
        try { obj = JSON.parse(trimmed); } catch (e) { return line; }
        if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) return line;

        const parts = [];
        const ts = formatTimestamp(obj.ts);
        if (ts) parts.push(ts);

        const level = String(obj.level || '').toUpperCase();
        if (level) parts.push(`[${level}]`);

        const event = obj.event ? String(obj.event) : '';
        const message = obj.message ? String(obj.message) : '';
        let body = message;
        if (event && !message.startsWith(event)) {
            body = event + (message ? ': ' + message : '');
        }
        if (body) parts.push(body);

        // 補足情報（hash 等のノイズは除く）。
        const extras = [];
        for (const [k, v] of Object.entries(obj)) {
            if (SKIP_KEYS.has(k)) continue;
            if (v === null || v === undefined || v === '') continue;
            let sv;
            if (Array.isArray(v)) sv = v.join(',');
            else if (typeof v === 'object') sv = JSON.stringify(v);
            else sv = String(v);
            extras.push(`${k}=${sv}`);
        }
        if (extras.length) parts.push('(' + extras.join(', ') + ')');

        return parts.join(' ');
    }

    function formatLogText(text) {
        return text.split('\n').map(formatJSONLine).join('\n');
    }

    // ============================================================
    // テーマ
    // ============================================================
    const themeToggle = document.getElementById('themeToggle');
    const themeIcon = document.getElementById('themeIcon');
    const themeLabel = document.getElementById('themeLabel');

    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        try { localStorage.setItem('kizuna-theme', theme); } catch (e) { /* ignore */ }
        if (themeIcon) themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
    }

    let savedTheme = null;
    try { savedTheme = localStorage.getItem('kizuna-theme'); } catch (e) { /* ignore */ }
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

    // 現在のタイプに応じてボタンの active を付け替える。
    function updateTypeButtons() {
        if (typeAgentBtn) typeAgentBtn.classList.toggle('active', currentType === 'agent');
        if (typeDashboardBtn) typeDashboardBtn.classList.toggle('active', currentType === 'dashboard');
        dynamicTypeButtons.forEach((btn, id) => btn.classList.toggle('active', currentType === id));
    }

    function setType(type) {
        currentType = type;
        updateTypeButtons();
        loadLogs();
    }

    // /api/logs/types からプラグイン等のログ種別を取得し、ボタンを追加する。
    async function loadLogTypes() {
        if (!typeButtonsEl) return;
        try {
            const res = await fetch('/api/logs/types');
            if (!res.ok) return;
            const data = await res.json();
            const list = Array.isArray(data.types) ? data.types : [];
            // 動的に追加したボタンだけを外す（Agent/Dashboard は残す）。
            dynamicTypeButtons.forEach((btn) => btn.remove());
            dynamicTypeButtons.clear();
            for (const item of list) {
                if (!item || !item.id) continue;
                const btn = document.createElement('button');
                btn.type = 'button';
                btn.className = 'btn-secondary';
                btn.textContent = item.label || item.id;
                btn.addEventListener('click', () => setType(item.id));
                typeButtonsEl.appendChild(btn);
                dynamicTypeButtons.set(item.id, btn);
            }
            updateTypeButtons();
        } catch (e) { /* 種別取得失敗は致命的ではない */ }
    }

    // updateFollowLabel keeps the follow button text in sync with the current
    // language (the button is not data-i18n because it toggles dynamically).
    function updateFollowLabel() {
        if (!followBtn) return;
        followBtn.textContent = follow ? t('logs.follow_on') : t('logs.follow_off');
        followBtn.classList.toggle('active', follow);
    }

    // 表示形式ボタンのラベルを現在の言語とモードに合わせる。
    function updateFormatLabel() {
        if (!formatBtn) return;
        // 押すと切り替わる先を表示する（今が整形なら「JSON」、今が JSON なら「整形」）。
        formatBtn.textContent = formatMode === 'pretty' ? t('logs.format_to_raw') : t('logs.format_to_pretty');
    }

    // trimToMaxLines は表示テキストを行数上限に収める（古い行から捨てる）。
    function trimToMaxLines(text) {
        if (!text) return text;
        const lines = text.split('\n');
        if (lines.length <= MAX_VIEW_LINES) return text;
        return lines.slice(lines.length - MAX_VIEW_LINES).join('\n');
    }

    async function loadLogs() {
        const lines = logLines ? logLines.value : '100';
        if (logStatus) logStatus.textContent = t('logs.loading');
        try {
            const res = await fetch(`/api/logs?type=${encodeURIComponent(currentType)}&lines=${encodeURIComponent(lines)}`);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const text = await res.text();
            if (logViewer) {
                if (!text) {
                    logViewer.textContent = t('logs.no_logs');
                } else {
                    const display = formatMode === 'pretty' ? formatLogText(text) : text;
                    logViewer.textContent = trimToMaxLines(display);
                }
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

    function startFollowTimer() {
        if (followTimer) clearInterval(followTimer);
        followTimer = setInterval(loadLogs, 3000);
    }

    function stopFollowTimer() {
        if (followTimer) {
            clearInterval(followTimer);
            followTimer = null;
        }
    }

    function toggleFollow() {
        follow = !follow;
        try { localStorage.setItem(FOLLOW_KEY, follow ? 'on' : 'off'); } catch (e) { /* ignore */ }
        updateFollowLabel();
        if (follow) startFollowTimer();
        else stopFollowTimer();
    }

    function toggleFormat() {
        formatMode = formatMode === 'pretty' ? 'raw' : 'pretty';
        try { localStorage.setItem(FORMAT_KEY, formatMode); } catch (e) { /* ignore */ }
        updateFormatLabel();
        loadLogs();
    }

    if (reloadBtn) reloadBtn.addEventListener('click', loadLogs);
    if (followBtn) followBtn.addEventListener('click', toggleFollow);
    if (formatBtn) formatBtn.addEventListener('click', toggleFormat);
    if (typeAgentBtn) typeAgentBtn.addEventListener('click', () => setType('agent'));
    if (typeDashboardBtn) typeDashboardBtn.addEventListener('click', () => setType('dashboard'));
    if (logLines) logLines.addEventListener('change', loadLogs);

    window.addEventListener('kizuna-lang-change', () => {
        applyTheme(document.documentElement.getAttribute('data-theme') || 'dark');
        updateFollowLabel();
        updateFormatLabel();
        loadLogs();
    });

    // 初期化
    loadLogTypes();
    updateFollowLabel();
    updateFormatLabel();
    if (follow) startFollowTimer();
    loadLogs();
})();
