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
    const searchInput = document.getElementById('logSearch');
    const searchRegex = document.getElementById('logSearchRegex');
    const levelFilter = document.getElementById('logLevelFilter');
    const eventFilter = document.getElementById('logEventFilter');
    const matchCount = document.getElementById('logMatchCount');

    // i18n 翻訳ヘルパー
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    // 表示は最大 500 行。超えたら古い行から捨てる。
    const MAX_VIEW_LINES = 500;
    const FOLLOW_KEY = 'kizuna-logs-follow';
    const FORMAT_KEY = 'kizuna-logs-format';

    // 自動更新は常に ON で開始する。運用者がページを開いた時点で
    // リアルタイム表示になっているのが期待動作で、前回の OFF を
    // 引き継ぐと「新しい行が出ない」と誤解される。
    let follow = true;

    let formatMode = 'pretty';
    try {
        const saved = localStorage.getItem(FORMAT_KEY);
        if (saved === 'raw') formatMode = 'raw';
        else if (saved === 'pretty') formatMode = 'pretty';
    } catch (e) { /* ignore */ }

    let currentType = 'agent';
    let followTimer = null;
    let eventSource = null;
    // 生ログ行のバッファ（検索/フィルタで再描画するため保持）。
    let rawLines = [];
    // イベント候補の蓄積（eventフィールドの値）。
    const seenEvents = new Set();

    const dynamicTypeButtons = new Map();

    // ============================================================
    // ログの整形（JSON Lines を人間向けに変換）
    // ============================================================
    const SKIP_KEYS = new Set(['hash', 'prev_hash', 'ts', 'level', 'message', 'message_en', 'prefix', 'event', 'icon']);

    function pad2(n) { return String(n).padStart(2, '0'); }

    function formatTimestamp(ts) {
        if (!ts) return '';
        const d = new Date(ts);
        if (isNaN(d.getTime())) return String(ts);
        return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ` +
               `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
    }

    // parseLogLine returns the parsed object (or null) and the raw text.
    function parseLogLine(line) {
        const trimmed = (line || '').trim();
        if (!trimmed || trimmed[0] !== '{') return null;
        try {
            const obj = JSON.parse(trimmed);
            if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) return null;
            return obj;
        } catch (e) { return null; }
    }

    // logLang returns the current UI language, defaulting to ja.
    function logLang() {
        return window.KizunaI18n ? window.KizunaI18n.getLang() : 'ja';
    }

    // pickLogMessage chooses message_en when the UI is English and the key
    // exists, otherwise the base message (task 9).
    function pickLogMessage(obj) {
        if (logLang() === 'en' && obj.message_en) return String(obj.message_en);
        return obj.message ? String(obj.message) : '';
    }

    function formatJSONLine(line) {
        const obj = parseLogLine(line);
        if (!obj) return line;
        const parts = [];
        const ts = formatTimestamp(obj.ts);
        if (ts) parts.push(ts);
        const level = String(obj.level || '').toUpperCase();
        if (level) parts.push(`[${level}]`);
        const event = obj.event ? String(obj.event) : '';
        const message = pickLogMessage(obj);
        let body = message;
        if (event && !message.startsWith(event)) {
            body = event + (message ? ': ' + message : '');
        }
        if (body) parts.push(body);
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

    // ============================================================
    // 検索 / フィルタ
    // ============================================================
    function getLevelOf(line) {
        const obj = parseLogLine(line);
        if (!obj) return '';
        return String(obj.level || '').toLowerCase();
    }

    function getEventOf(line) {
        const obj = parseLogLine(line);
        if (!obj) return '';
        return String(obj.event || '');
    }

    // buildMatcher returns a predicate for the search box, or null when empty.
    function buildMatcher() {
        const q = searchInput ? searchInput.value.trim() : '';
        if (!q) return null;
        if (searchRegex && searchRegex.checked) {
            try {
                const re = new RegExp(q, 'i');
                return (line) => re.test(line);
            } catch (e) {
                return null; // 不正な正規表現はマッチなし扱い（下で0件表示）
            }
        }
        const lower = q.toLowerCase();
        return (line) => line.toLowerCase().indexOf(lower) >= 0;
    }

    function applyFilters(lines) {
        const matcher = buildMatcher();
        const lv = levelFilter ? levelFilter.value : '';
        const ev = eventFilter ? eventFilter.value : '';
        let out = lines;
        if (lv) out = out.filter(l => getLevelOf(l) === lv);
        if (ev) out = out.filter(l => getEventOf(l) === ev);
        if (matcher) out = out.filter(matcher);
        return out;
    }

    // highlight applies <mark> around search matches (case-insensitive).
    function highlight(text) {
        const q = searchInput ? searchInput.value.trim() : '';
        const escaped = escapeHtml(text);
        if (!q) return escaped;
        let pattern;
        if (searchRegex && searchRegex.checked) {
            // 正規表現でハイライト。ユーザー入力をそのまま使うため、
            // キャプチャ崩れを避けて全体を1グループで囲む。
            try {
                const re = new RegExp(q, 'gi');
                return escaped.replace(re, (m) => `<mark>${m}</mark>`);
            } catch (e) { return escaped; }
        }
        const qEsc = q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        try {
            const re = new RegExp('(' + qEsc + ')', 'gi');
            return escaped.replace(re, '<mark>$1</mark>');
        } catch (e) { return escaped; }
    }

    // collectEvents accumulates event values for the event filter dropdown.
    function collectEvents(lines) {
        let added = false;
        for (const line of lines) {
            const ev = getEventOf(line);
            if (ev && !seenEvents.has(ev)) {
                seenEvents.add(ev);
                added = true;
            }
        }
        if (added && eventFilter) {
            const current = eventFilter.value;
            // 既存の選択肢をクリアして再構築（「すべて」は残す）。
            while (eventFilter.options.length > 1) eventFilter.remove(1);
            Array.from(seenEvents).sort().forEach(ev => {
                const opt = document.createElement('option');
                opt.value = ev;
                opt.textContent = ev;
                eventFilter.appendChild(opt);
            });
            eventFilter.value = current;
        }
    }

    // render re-applies filters and paints the viewer.
    function render() {
        if (!logViewer) return;
        const filtered = applyFilters(rawLines);
        const shown = filtered.slice(-MAX_VIEW_LINES);
        if (matchCount) {
            matchCount.textContent = rawLines.length
                ? `${shown.length} / ${rawLines.length}`
                : '';
        }
        if (shown.length === 0) {
            logViewer.innerHTML = `<span class="log-empty">${escapeHtml(t('logs.no_logs'))}</span>`;
            return;
        }
        // 最新が上に来るよう逆順（降順）で表示する。末尾に追記された
        // 行が一番上に現れ、下に行くほど古い。
        const html = shown.slice().reverse().map(line => {
            const pretty = formatMode === 'pretty' ? formatJSONLine(line) : line;
            return highlight(pretty);
        }).join('\n');
        logViewer.innerHTML = html;
        // 追従中は最新（先頭）が見えるようトップへスクロールする。
        if (follow) logViewer.scrollTop = 0;
    }

    function setRawText(text) {
        rawLines = text ? text.split('\n').filter(l => l !== '') : [];
        collectEvents(rawLines);
        render();
    }

    function appendLine(line) {
        if (!line) return;
        rawLines.push(line);
        if (rawLines.length > MAX_VIEW_LINES * 4) {
            rawLines = rawLines.slice(-MAX_VIEW_LINES * 2);
        }
        const ev = getEventOf(line);
        if (ev && !seenEvents.has(ev)) {
            seenEvents.add(ev);
            if (eventFilter) {
                const opt = document.createElement('option');
                opt.value = ev;
                opt.textContent = ev;
                eventFilter.appendChild(opt);
            }
        }
        render();
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

    function updateTypeButtons() {
        if (typeAgentBtn) typeAgentBtn.classList.toggle('active', currentType === 'agent');
        if (typeDashboardBtn) typeDashboardBtn.classList.toggle('active', currentType === 'dashboard');
        dynamicTypeButtons.forEach((btn, id) => btn.classList.toggle('active', currentType === id));
    }

    function setType(type) {
        currentType = type;
        // 種別を変えたらイベント候補をリセット。
        seenEvents.clear();
        if (eventFilter) {
            while (eventFilter.options.length > 1) eventFilter.remove(1);
        }
        updateTypeButtons();
        startStream();
    }

    // capitalizeLabel renders a type id as a display label: "kizuna-security"
    // -> "Kizuna-Security", "agent" -> "Agent". The dashboard/product names
    // elsewhere start with a capital, so the log type buttons should match.
    function capitalizeLabel(s) {
        return String(s || '').split(/([-_])/).map(part =>
            part.length > 0 && part !== '-' && part !== '_'
                ? part.charAt(0).toUpperCase() + part.slice(1)
                : part
        ).join('');
    }

    async function loadLogTypes() {
        if (!typeButtonsEl) return;
        try {
            const res = await fetch('/api/logs/types');
            if (!res.ok) return;
            const data = await res.json();
            const list = Array.isArray(data.types) ? data.types : [];
            dynamicTypeButtons.forEach((btn) => btn.remove());
            dynamicTypeButtons.clear();
            for (const item of list) {
                if (!item || !item.id) continue;
                const btn = document.createElement('button');
                btn.type = 'button';
                btn.className = 'btn-secondary';
                btn.textContent = capitalizeLabel(item.label || item.id);
                btn.addEventListener('click', () => setType(item.id));
                typeButtonsEl.appendChild(btn);
                dynamicTypeButtons.set(item.id, btn);
            }
            updateTypeButtons();
        } catch (e) { /* 種別取得失敗は致命的ではない */ }
    }

    function updateFollowLabel() {
        if (!followBtn) return;
        // ON のときは CSS（#logFollowBtn.follow-on）が緑背景と点滅ドットを
        // 付ける。テキストだけでは現在の状態が一目で分からず、更新が
        // 止まっているように見えるという指摘への対応。
        followBtn.textContent = follow ? t('logs.follow_on') : t('logs.follow_off');
        followBtn.classList.toggle('active', follow);
        followBtn.classList.toggle('follow-on', follow);
    }

    function updateFormatLabel() {
        if (!formatBtn) return;
        formatBtn.textContent = formatMode === 'pretty' ? t('logs.format_to_raw') : t('logs.format_to_pretty');
    }

    // ============================================================
    // 取得: 初回は /api/logs（tail）、以降は SSE /api/logs/stream で
    // 追記をリアルタイム受信する。SSE が使えない環境では従来どおり
    // ポーリングへフォールバックする。
    // ============================================================
    async function loadInitial() {
        const lines = logLines ? logLines.value : '100';
        if (logStatus) logStatus.textContent = t('logs.loading');
        try {
            const res = await fetch(`/api/logs?type=${encodeURIComponent(currentType)}&lines=${encodeURIComponent(lines)}`);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const text = await res.text();
            setRawText(text);
            if (logStatus) {
                logStatus.textContent = `${currentType}: ${lines} / ${new Date().toLocaleTimeString()}`;
            }
        } catch (e) {
            if (logViewer) logViewer.innerHTML = `<span class="log-empty">${escapeHtml(t('status.error'))}: ${escapeHtml(e.message)}</span>`;
            if (logStatus) logStatus.textContent = t('status.error');
        }
    }

    function closeStream() {
        if (eventSource) {
            try { eventSource.close(); } catch (e) { /* ignore */ }
            eventSource = null;
        }
        if (followTimer) {
            clearInterval(followTimer);
            followTimer = null;
        }
    }

    function startPollingFallback() {
        if (followTimer) clearInterval(followTimer);
        followTimer = setInterval(loadInitial, 3000);
    }

    function startStream() {
        closeStream();
        // まず tail を読み込む。
        loadInitial().then(() => {
            if (!window.EventSource) {
                if (follow) startPollingFallback();
                return;
            }
            // SSE で追記を受信。
            try {
                const url = `/api/logs/stream?type=${encodeURIComponent(currentType)}`;
                const es = new EventSource(url);
                eventSource = es;
                es.onmessage = (ev) => {
                    if (ev && typeof ev.data === 'string') appendLine(ev.data);
                };
                es.onerror = () => {
                    // SSE が切れた/使えない: ポーリングへフォールバック。
                    closeStream();
                    if (follow) startPollingFallback();
                };
                if (logStatus) {
                    logStatus.textContent = `${currentType}: streaming / ${new Date().toLocaleTimeString()}`;
                }
            } catch (e) {
                if (follow) startPollingFallback();
            }
        });
    }

    function toggleFollow() {
        follow = !follow;
        try { localStorage.setItem(FOLLOW_KEY, follow ? 'on' : 'off'); } catch (e) { /* ignore */ }
        updateFollowLabel();
        if (follow) {
            if (eventSource || followTimer) {
                // 既に動いている
            } else {
                startStream();
            }
        } else {
            closeStream();
        }
    }

    function toggleFormat() {
        formatMode = formatMode === 'pretty' ? 'raw' : 'pretty';
        try { localStorage.setItem(FORMAT_KEY, formatMode); } catch (e) { /* ignore */ }
        updateFormatLabel();
        render();
    }

    if (reloadBtn) reloadBtn.addEventListener('click', () => startStream());
    if (followBtn) followBtn.addEventListener('click', toggleFollow);
    if (formatBtn) formatBtn.addEventListener('click', toggleFormat);
    if (typeAgentBtn) typeAgentBtn.addEventListener('click', () => setType('agent'));
    if (typeDashboardBtn) typeDashboardBtn.addEventListener('click', () => setType('dashboard'));
    if (logLines) logLines.addEventListener('change', () => startStream());

    // 検索・フィルタは再描画のみ（再取得しない）。
    if (searchInput) searchInput.addEventListener('input', render);
    if (searchRegex) searchRegex.addEventListener('change', render);
    if (levelFilter) levelFilter.addEventListener('change', render);
    if (eventFilter) eventFilter.addEventListener('change', render);

    window.addEventListener('kizuna-lang-change', () => {
        applyTheme(document.documentElement.getAttribute('data-theme') || 'dark');
        updateFollowLabel();
        updateFormatLabel();
        render();
    });

    // 初期化
    loadLogTypes();
    updateFollowLabel();
    updateFormatLabel();
    startStream();
    if (!follow) closeStream();
})();
