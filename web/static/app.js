// ============================================================
// Kizuna-Eye Dashboard
// Gauge Engine v0.7.0
// ============================================================

(function () {
    'use strict';

    const VERSION = 'v0.7.0';

    const GAUGE = {
        radiusRatio: 0.40,
        lineWidthRatio: 0.20,
        glowBlur: 15
    };

    const ALERT = {
        mem:      { warn: 80, critical: 90 },
        diskFree: { warn: 20, critical: 10 },
        cpuTemp:  { warn: 70, critical: 85 },
        cpu:      { warn: 80, critical: 95 },
        holdMs:   5000,
        cooldownMs: 5 * 60 * 1000
    };

    const $ = (selector) => document.querySelector(selector);

    // i18n 翻訳ヘルパー（i18n.js 未読込時はキーをそのまま返す）
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    // pickLocalized returns the field matching the current UI language.
    // For English it prefers field_en, falling back to the base field
    // (Japanese) when the English rendering is absent — older records only
    // have the base field, so falling back keeps them visible (task 9).
    function pickLocalized(base, en) {
        const lang = window.KizunaI18n ? window.KizunaI18n.getLang() : 'ja';
        if (lang === 'en' && en) return en;
        return base || '';
    }

    const elements = {
        connectionStatus: $('#connectionStatus'),
        cpuGauge: $('#cpuGauge'),
        cpuValue: $('#cpuValue'),
        cpuDetail: $('#cpuDetail'),
        cpuCores: $('#cpuCores'),
        cpuModel: $('#cpuModel'),
        cpuTempBadge: $('#cpuTempBadge'),
        cpuCard: $('#cpuCard'),
        memGauge: $('#memGauge'),
        memValue: $('#memValue'),
        memDetail: $('#memDetail'),
        memFreeBadge: $('#memFreeBadge'),
        memCard: $('#memCard'),
        diskGauge: $('#diskGauge'),
        diskValue: $('#diskValue'),
        diskDetail: $('#diskDetail'),
        diskList: $('#diskList'),
        diskTempBadge: $('#diskTempBadge'),
        diskFreeBadge: $('#diskFreeBadge'),
        diskCard: $('#diskCard'),
        uptimeValue: $('#uptimeValue'),
        uptimeLabel: $('#uptimeLabel'),
        uptimeDetail: $('#uptimeDetail'),
        bootTime: $('#bootTime'),
        loadAvg: $('#loadAvg'),
        networkIO: $('#networkIO'),
        backupSection: $('#backupSection'),
        backupStatusBadge: $('#backupStatusBadge'),
        backupToggle: $('#backupToggle'),
        backupBody: $('#backupBody'),
        moduleCount: $('#moduleCount'),
        pluginList: $('#pluginList'),
        alertsSection: $('#alertsSection'),
        alertsToggle: $('#alertsToggle'),
        alertsBody: $('#alertsBody'),
        alertsCountBadge: $('#alertsCountBadge'),
        alertsClearBtn: $('#alertsClearBtn'),
        alertList: $('#alertList'),
        historySection: $('#historySection'),
        historyToggle: $('#historyToggle'),
        historyBody: $('#historyBody'),
        historyChart: $('#historyChart'),
        historyRangeBadge: $('#historyRangeBadge'),
        themeToggle: $('#themeToggle'),
        themeIcon: $('#themeIcon'),
        themeLabel: $('#themeLabel'),
        versionBadge: $('#versionBadge'),
        footerVersion: $('#footerVersion'),
        toastContainer: $('#toastContainer'),
        // ★ Phase 6
        processModal: $('#processModal'),
        processModalTitle: $('#processModalTitle'),
        processModalClose: $('#processModalClose'),
        processSearch: $('#processSearch'),
        processTableBody: $('#processTableBody'),
        processHint: $('#processHint'),
        // ★ ストレージ詳細モーダル
        storageModal: $('#storageModal'),
        storageModalClose: $('#storageModalClose'),
        storageList: $('#storageList'),
        storageHint: $('#storageHint')
    };

    if (elements.versionBadge) elements.versionBadge.textContent = VERSION;
    if (elements.footerVersion) elements.footerVersion.textContent = VERSION;

    // ============================================================
    // ★ Phase 6: プロセスモーダル用の状態
    // ============================================================
    const processState = {
        list: [],           // 最新のプロセス一覧（CPU降順ソート済み）
        sortKey: 'cpu',     // 'cpu' | 'mem'
        search: '',         // 検索クエリ
        isOpen: false
    };

    // ============================================================
    // 接続状態
    // ============================================================
    let currentConnectionState = 'connecting';

    function setConnectionState(state) {
        currentConnectionState = state;
        if (!elements.connectionStatus) return;
        const el = elements.connectionStatus;
        el.classList.remove('connected', 'disconnected', 'connecting');
        el.classList.add(state);
        switch (state) {
            case 'connected':    el.textContent = t('status.connected');   break;
            case 'connecting':   el.textContent = t('status.connecting'); break;
            case 'disconnected': el.textContent = t('status.disconnected'); break;
            default:             el.textContent = state;
        }
    }

    // ============================================================
    // テーマ
    // ============================================================
    function getTheme() {
        return document.documentElement.getAttribute('data-theme') || 'dark';
    }

    function setTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        // localStorage はプライベートモード等で例外を投げる。失敗しても
        // 初期化 IIFE 全体（イベント登録・データ取得）を止めないよう握りつぶす。
        try { localStorage.setItem('kizuna-theme', theme); } catch (e) { /* ignore */ }

        if (elements.themeIcon) {
            elements.themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        }
        if (elements.themeLabel) {
            elements.themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
        }

        if (window._lastData) updateDashboard(window._lastData);
    }

    function toggleTheme() {
        const current = getTheme();
        setTheme(current === 'dark' ? 'light' : 'dark');
    }

    // Phase 9-2: OS設定に基づく自動テーマ切り替え
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)');
    let savedTheme = null;
    try { savedTheme = localStorage.getItem('kizuna-theme'); } catch (e) { /* ignore */ }

    // localStorageに保存がある場合はそれを使用
    // 保存がない場合はOS設定を使用
    if (savedTheme) {
        setTheme(savedTheme);
    } else {
        setTheme(prefersDark.matches ? 'dark' : 'light');
    }

    // OS設定の変更を監視（localStorageに手動設定がない場合のみ）
    prefersDark.addEventListener('change', (e) => {
        let manual = null;
        try { manual = localStorage.getItem('kizuna-theme'); } catch (err) { /* ignore */ }
        if (!manual) {
            setTheme(e.matches ? 'dark' : 'light');
        }
    });

    if (elements.themeToggle) {
        elements.themeToggle.addEventListener('click', toggleTheme);
    }

    // ============================================================
    // ユーティリティ
    // ============================================================
    function clamp(value, min, max) {
        return Math.min(Math.max(Number(value) || 0, min), max);
    }

    // escapeHtml / escapeAttr は escape.js（共有ユーティリティ）で定義される。
    // プラグイン・ログ由来の文字列を innerHTML へ入れる前に必ず通すこと。

    // ============================================================
    // フォーマット
    // ============================================================
    function formatBytes(bytes) {
        bytes = Number(bytes) || 0;
        if (bytes === 0) return '0 B';
        const units = ['B', 'KB', 'MB', 'GB', 'TB'];
        const k = 1024;
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        const value = bytes / Math.pow(k, i);
        return value.toFixed(i === 0 ? 0 : 1) + ' ' + units[Math.min(i, units.length - 1)];
    }

    function formatBytesFixed(bytes, unit) {
        bytes = Number(bytes) || 0;
        const multipliers = {
            'B': 1, 'KB': 1024, 'MB': 1024 * 1024,
            'GB': 1024 * 1024 * 1024, 'TB': 1024 * 1024 * 1024 * 1024
        };
        const mult = multipliers[unit];
        if (!mult) return formatBytes(bytes);
        const value = bytes / mult;
        const decimals = (unit === 'GB' || unit === 'TB') ? 2 : 1;
        return value.toFixed(decimals) + ' ' + unit;
    }

    function formatStorage(bytes) {
        bytes = Number(bytes) || 0;
        const GB = 1024 * 1024 * 1024;
        const TB = GB * 1024;
        if (bytes >= TB) return (bytes / TB).toFixed(2) + ' TB';
        return (bytes / GB).toFixed(1) + ' GB';
    }

    function formatSpeed(bytesPerSec) {
        const bps = Number(bytesPerSec) || 0;
        if (bps === 0) return '0 B/s';
        const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
        const k = 1024;
        const i = Math.floor(Math.log(bps) / Math.log(k));
        const value = bps / Math.pow(k, i);
        return value.toFixed(i === 0 ? 0 : 1) + ' ' + units[Math.min(i, units.length - 1)];
    }

    function formatMemMB(mb) {
        mb = Number(mb) || 0;
        if (mb < 1024) return mb.toFixed(1) + ' MB';
        return (mb / 1024).toFixed(2) + ' GB';
    }

    function formatUptime(seconds) {
        seconds = Math.max(0, Number(seconds) || 0);
        const days = Math.floor(seconds / 86400);
        const hours = Math.floor((seconds % 86400) / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        const secs = Math.floor(seconds % 60);

        if (days > 0) return { value: t('uptime.days_hours', days, hours), unit: '' };
        if (hours > 0) return { value: t('uptime.hours_minutes', hours, minutes), unit: '' };
        if (minutes > 0) return { value: t('uptime.minutes_seconds', minutes, secs), unit: '' };
        return { value: t('uptime.seconds', secs), unit: '' };
    }

    function formatBootTime(uptimeSeconds) {
        const secs = Math.max(0, Number(uptimeSeconds) || 0);
        if (secs === 0) return '--';
        const boot = new Date(Date.now() - secs * 1000);
        const y = boot.getFullYear();
        const m = String(boot.getMonth() + 1).padStart(2, '0');
        const d = String(boot.getDate()).padStart(2, '0');
        const hh = String(boot.getHours()).padStart(2, '0');
        const mm = String(boot.getMinutes()).padStart(2, '0');
        return `${y}/${m}/${d} ${hh}:${mm}`;
    }

    function formatClock(date) {
        const hh = String(date.getHours()).padStart(2, '0');
        const mm = String(date.getMinutes()).padStart(2, '0');
        const ss = String(date.getSeconds()).padStart(2, '0');
        return `${hh}:${mm}:${ss}`;
    }

    // ============================================================
    // トースト通知
    // ============================================================
    function showToast(message, type = 'info') {
        if (!elements.toastContainer) return;
        const el = document.createElement('div');
        el.className = `dashboard-toast ${type}`;
        el.innerHTML = `
            <span class="toast-message">${escapeHtml(message)}</span>
        `;
        elements.toastContainer.appendChild(el);

        setTimeout(() => {
            el.style.opacity = '0';
            el.style.transform = 'translateX(100px)';
            el.style.transition = 'opacity 0.3s, transform 0.3s';
            setTimeout(() => el.remove(), 300);
        }, 8000);
    }

    // ============================================================
    // アラートエンジン
    // ============================================================
    const alertState = {
        mem:      { exceedSince: null, lastNotified: 0 },
        diskFree: { exceedSince: null, lastNotified: 0 },
        cpuTemp:  { exceedSince: null, lastNotified: 0 },
        cpu:      { exceedSince: null, lastNotified: 0 }
    };

    function resetAlert(key) {
        alertState[key].exceedSince = null;
    }

    function evaluateAlert(key, value, isExceeded) {
        const state = alertState[key];
        const now = Date.now();

        if (!isExceeded) {
            resetAlert(key);
            return false;
        }
        if (state.exceedSince === null) {
            state.exceedSince = now;
            return false;
        }
        if (now - state.exceedSince < ALERT.holdMs) return false;
        if (now - state.lastNotified < ALERT.cooldownMs) return false;

        state.lastNotified = now;
        return true;
    }

    function applyCardAlert(cardEl, level) {
        if (!cardEl) return;
        cardEl.classList.remove('alert-warn', 'alert-critical');
        if (level === 'warn')     cardEl.classList.add('alert-warn');
        if (level === 'critical') cardEl.classList.add('alert-critical');
    }

    // Sync the frontend alert thresholds with the server-side settings
    // so the card colors / toasts match the notification engine.
    async function loadAlertThresholds() {
        try {
            const res = await fetch('/api/alert-config');
            if (!res.ok) return;
            const c = await res.json();
            if (typeof c.memory_warn_pct === 'number') ALERT.mem.warn = c.memory_warn_pct;
            if (typeof c.memory_critical_pct === 'number') ALERT.mem.critical = c.memory_critical_pct;
            if (typeof c.disk_free_warn_pct === 'number') ALERT.diskFree.warn = c.disk_free_warn_pct;
            if (typeof c.disk_free_critical_pct === 'number') ALERT.diskFree.critical = c.disk_free_critical_pct;
            if (typeof c.cpu_temp_warn_c === 'number') ALERT.cpuTemp.warn = c.cpu_temp_warn_c;
            if (typeof c.cpu_temp_critical_c === 'number') ALERT.cpuTemp.critical = c.cpu_temp_critical_c;
        } catch (e) {
            // 閾値取得失敗は致命的ではないので無視
        }
    }

    function checkAlerts(cpuPercent, cpuTemp, memPercent, diskFreePercent) {
        let memLevel = null;
        if (memPercent >= ALERT.mem.critical) memLevel = 'critical';
        else if (memPercent >= ALERT.mem.warn) memLevel = 'warn';
        applyCardAlert(elements.memCard, memLevel);
        if (evaluateAlert('mem', memPercent, memPercent >= ALERT.mem.critical)) {
            showToast(t('toast.mem_high', memPercent.toFixed(1)), 'error');
        }

        let diskLevel = null;
        if (diskFreePercent <= ALERT.diskFree.critical) diskLevel = 'critical';
        else if (diskFreePercent <= ALERT.diskFree.warn) diskLevel = 'warn';
        applyCardAlert(elements.diskCard, diskLevel);
        if (evaluateAlert('diskFree', diskFreePercent, diskFreePercent <= ALERT.diskFree.critical)) {
            showToast(t('toast.disk_low', diskFreePercent.toFixed(1)), 'error');
        }

        let tempLevel = null;
        if (cpuTemp >= ALERT.cpuTemp.critical) tempLevel = 'critical';
        else if (cpuTemp >= ALERT.cpuTemp.warn) tempLevel = 'warn';
        let cpuLevel = null;
        if (cpuPercent >= ALERT.cpu.critical) cpuLevel = 'critical';
        else if (cpuPercent >= ALERT.cpu.warn) cpuLevel = 'warn';

        const finalCpuLevel = (tempLevel === 'critical' || cpuLevel === 'critical') ? 'critical'
                            : (tempLevel === 'warn' || cpuLevel === 'warn') ? 'warn'
                            : null;
        applyCardAlert(elements.cpuCard, finalCpuLevel);

        if (evaluateAlert('cpuTemp', cpuTemp, cpuTemp >= ALERT.cpuTemp.critical)) {
            showToast(t('toast.cpu_temp_high', Math.round(cpuTemp)), 'error');
        }
        if (evaluateAlert('cpu', cpuPercent, cpuPercent >= ALERT.cpu.critical)) {
            showToast(t('toast.cpu_high', cpuPercent.toFixed(1)), 'warning');
        }
    }

    // ============================================================
    // 色
    // ============================================================
    function getGaugeColors(type) {
        const root = getComputedStyle(document.documentElement);
        if (type === 'mem') {
            return {
                start:  root.getPropertyValue('--gauge-mem-start').trim()  || '#3478ff',
                middle: root.getPropertyValue('--gauge-mem-mid').trim()    || '#7b3ff2',
                end:    root.getPropertyValue('--gauge-mem-end').trim()    || '#ff3970'
            };
        }
        if (type === 'disk') {
            return {
                start:  root.getPropertyValue('--gauge-disk-start').trim() || '#a855f7',
                middle: root.getPropertyValue('--gauge-disk-mid').trim()   || '#d946ef',
                end:    root.getPropertyValue('--gauge-disk-end').trim()   || '#ec4899'
            };
        }
        return {
            start:  root.getPropertyValue('--gauge-cpu-start').trim() || '#18d66b',
            middle: root.getPropertyValue('--gauge-cpu-mid').trim()   || '#ffc400',
            end:    root.getPropertyValue('--gauge-cpu-end').trim()   || '#ff4b22'
        };
    }

    function createGaugeGradient(ctx, cx, cy, radius, colors) {
        const gradient = ctx.createLinearGradient(cx - radius, cy, cx + radius, cy);
        gradient.addColorStop(0, colors.start);
        gradient.addColorStop(0.50, colors.middle);
        gradient.addColorStop(1, colors.end);
        return gradient;
    }

    // ============================================================
    // メインゲージ
    // ============================================================
    function drawGauge(canvas, percent, type) {
        if (!canvas) return;
        const ctx = canvas.getContext('2d');
        if (!ctx) return;

        const width = canvas.width;
        const height = canvas.height;
        ctx.clearRect(0, 0, width, height);

        const cx = width / 2;
        const cy = height * 0.52;
        const radius = Math.min(width, height) * GAUGE.radiusRatio;
        const lineWidth = Math.max(10, radius * GAUGE.lineWidthRatio);
        const value = clamp(percent, 0, 100);

        const leftBottom = Math.PI * 0.75;
        const sweep = Math.PI * 1.5;
        const rightBottom = leftBottom + sweep;
        const fillEnd = leftBottom + sweep * (value / 100);

        ctx.save();
        ctx.beginPath();
        ctx.arc(cx, cy, radius, leftBottom, rightBottom, false);
        ctx.strokeStyle = 'rgba(90, 98, 120, 0.35)';
        ctx.lineWidth = lineWidth;
        ctx.lineCap = 'round';
        ctx.stroke();
        ctx.restore();

        if (value > 0) {
            const colors = getGaugeColors(type);
            const gradient = createGaugeGradient(ctx, cx, cy, radius, colors);

            ctx.save();
            ctx.beginPath();
            ctx.arc(cx, cy, radius, leftBottom, fillEnd, false);
            ctx.strokeStyle = gradient;
            ctx.lineWidth = lineWidth + 2;
            ctx.lineCap = 'round';
            ctx.shadowColor = colors.middle;
            ctx.shadowBlur = GAUGE.glowBlur;
            ctx.globalAlpha = 0.6;
            ctx.stroke();
            ctx.restore();

            ctx.save();
            ctx.beginPath();
            ctx.arc(cx, cy, radius, leftBottom, fillEnd, false);
            ctx.strokeStyle = gradient;
            ctx.lineWidth = lineWidth;
            ctx.lineCap = 'round';
            ctx.stroke();
            ctx.restore();
        }

        const fontSize = Math.max(11, radius * 0.11);
        const colors = getGaugeColors(type);

        const leftX = cx + Math.cos(Math.PI * 0.75) * radius;
        const leftY = cy + Math.sin(Math.PI * 0.75) * radius;
        const rightX = cx + Math.cos(Math.PI * 0.25) * radius;
        const rightY = cy + Math.sin(Math.PI * 0.25) * radius;

        ctx.save();
        ctx.font = `500 ${fontSize}px Inter, sans-serif`;
        ctx.fillStyle = 'rgba(160,170,195,0.8)';
        ctx.textBaseline = 'middle';
        ctx.textAlign = 'center';
        ctx.fillText('0%', leftX, leftY + fontSize * 1.6);
        ctx.fillText('100%', rightX, rightY + fontSize * 1.6);
        ctx.restore();
    }

    // ============================================================
    // ミニゲージ
    // ============================================================
    function drawMiniGauge(canvas, percent) {
        if (!canvas) return;
        const ctx = canvas.getContext('2d');
        if (!ctx) return;

        const w = canvas.width;
        const h = canvas.height;
        ctx.clearRect(0, 0, w, h);

        const cx = w / 2;
        const cy = h * 0.52;
        const radius = Math.min(w, h) * 0.32;
        const lineWidth = 6;
        const value = clamp(percent, 0, 100);

        const leftBottom = Math.PI * 0.75;
        const sweep = Math.PI * 1.5;
        const rightBottom = leftBottom + sweep;
        const fillEnd = leftBottom + sweep * (value / 100);

        ctx.save();
        ctx.beginPath();
        ctx.arc(cx, cy, radius, leftBottom, rightBottom, false);
        ctx.strokeStyle = 'rgba(100,108,130,0.25)';
        ctx.lineWidth = lineWidth;
        ctx.lineCap = 'round';
        ctx.stroke();
        ctx.restore();

        if (value > 0) {
            let color;
            if (value >= 85) color = '#ef4444';
            else if (value >= 60) color = '#f59e0b';
            else color = '#20d66b';

            ctx.save();
            ctx.beginPath();
            ctx.arc(cx, cy, radius, leftBottom, fillEnd, false);
            ctx.strokeStyle = color;
            ctx.lineWidth = lineWidth;
            ctx.lineCap = 'round';
            ctx.shadowColor = color;
            ctx.shadowBlur = 4;
            ctx.stroke();
            ctx.restore();
        }
    }

    // ============================================================
    // ツールチップ（data-tooltip 属性）
    // ============================================================
    // 祖先の overflow（.app の overflow-x: clip 等）で ::after 方式は
    // 見切れるため、body 直下に position: fixed の要素を出して
    // 位置を JS で計算する。
    (function setupTooltips() {
        const tip = document.createElement('div');
        tip.className = 'kizuna-tooltip';
        tip.hidden = true;
        document.body.appendChild(tip);

        function show(el) {
            const text = el.getAttribute('data-tooltip');
            if (!text) return;
            tip.textContent = text;
            tip.hidden = false;

            // 一旦測定してから、画面内に収まるよう位置を補正する。
            tip.style.left = '0px';
            tip.style.top = '0px';
            const r = el.getBoundingClientRect();
            const tr = tip.getBoundingClientRect();

            let left = r.right - tr.width;                       // 右端を要素に合わせる
            const margin = 8;
            if (left < margin) left = margin;
            if (left + tr.width > window.innerWidth - margin) {
                left = window.innerWidth - margin - tr.width;
            }

            let top = r.bottom + 6;
            if (top + tr.height > window.innerHeight - margin) {
                top = r.top - tr.height - 6;                     // 下に出せなければ上に
            }
            if (top < margin) top = margin;

            tip.style.left = left + 'px';
            tip.style.top = top + 'px';
        }

        function hide() { tip.hidden = true; }

        document.addEventListener('mouseover', (e) => {
            const el = e.target.closest && e.target.closest('[data-tooltip]');
            if (el) show(el);
        });
        document.addEventListener('mouseout', (e) => {
            const el = e.target.closest && e.target.closest('[data-tooltip]');
            if (el) hide();
        });
        document.addEventListener('focusin', (e) => {
            const el = e.target.closest && e.target.closest('[data-tooltip]');
            if (el) show(el);
        });
        document.addEventListener('focusout', hide);
        window.addEventListener('scroll', hide, true);
    })();

    // ============================================================
    // 温度バッジ
    // ============================================================
    const NA_TOOLTIP = () => t('card.temp.na.tooltip');

    function updateTempBadge(el, temp) {
        if (!el) return;
        const tempNum = Number(temp);

        if (!Number.isFinite(tempNum) || tempNum <= 25) {
            el.hidden = false;
            el.textContent = t('card.temp.na');
            el.classList.remove('cool', 'warm', 'hot', 'critical');
            el.classList.add('na');
            // title 属性は改行できないため、CSS ツールチップ用の data-tooltip に
            // 入れる（改行 \n は CSS の white-space: pre-line で表示される）。
            el.removeAttribute('title');
            el.setAttribute('data-tooltip', NA_TOOLTIP());
            return;
        }

        el.hidden = false;
        el.textContent = `${Math.round(tempNum)}℃`;
        el.removeAttribute('title');
        el.removeAttribute('data-tooltip');
        el.classList.remove('na', 'cool', 'warm', 'hot', 'critical');

        if (tempNum <= 45) el.classList.add('cool');
        else if (tempNum <= 65) el.classList.add('warm');
        else if (tempNum <= 85) el.classList.add('hot');
        else el.classList.add('critical');
    }

    // ============================================================
    // 空き容量バッジ
    // ============================================================
    // critFreePct / warnFreePct are free-space percentages. Memory and disk
    // use different thresholds, so they must be passed in explicitly (a memory
    // badge judged by disk thresholds would stay green while memory is
    // actually critical).
    function updateFreeBadge(el, freeBytes, totalBytes, critFreePct, warnFreePct) {
        if (!el) return;
        const free = Math.max(0, Number(freeBytes) || 0);
        const total = Math.max(0, Number(totalBytes) || 0);
        if (total === 0) { el.hidden = true; return; }

        const freePercent = (free / total) * 100;
        el.hidden = false;
        el.textContent = t('card.free', formatStorage(free));
        el.classList.remove('warn', 'critical');
        el.setAttribute('title', `Free: ${freePercent.toFixed(1)}%`);

        if (freePercent <= critFreePct) el.classList.add('critical');
        else if (freePercent <= warnFreePct) el.classList.add('warn');
    }

    // ============================================================
    // CPUコア
    // ============================================================
    function updateCpuCores(cores, freq) {
        if (!elements.cpuCores) return;
        if (!Array.isArray(cores) || cores.length === 0) {
            elements.cpuCores.innerHTML = '';
            return;
        }

        let freqs = [];
        if (Array.isArray(freq) && freq.length === cores.length) {
            freqs = freq;
        } else if (typeof freq === 'number' && freq > 0) {
            freqs = cores.map(() => freq);
        }

        let html = '';
        cores.forEach((value, index) => {
            const pct = clamp(value, 0, 100);
            const f = freqs[index] || 0;
            const freqText = f > 0 ? `${(f / 1000).toFixed(2)} GHz` : t('process.col_freq', 'N/A');
            html += `
                <div class="cpu-core-item">
                    <canvas id="core-gauge-${Number(index)}" width="64" height="64"></canvas>
                    <span class="core-value">${pct.toFixed(0)}%</span>
                    <span class="core-label">C${index + 1}</span>
                    <span class="core-tooltip">Core ${index + 1}<br>${pct.toFixed(1)}% / ${freqText}</span>
                </div>
            `;
        });

        elements.cpuCores.innerHTML = html;

        cores.forEach((value, index) => {
            const canvas = document.getElementById(`core-gauge-${index}`);
            if (canvas) drawMiniGauge(canvas, value);
        });
    }

    // ============================================================
    // ディスクリスト
    // ============================================================
    function updateDiskList(disks) {
        if (!elements.diskList) return;
        if (!Array.isArray(disks) || disks.length === 0) {
            elements.diskList.innerHTML = '';
            return;
        }

        let html = '';
        disks.forEach(disk => {
            const total = Number(disk.total) || 0;
            const used = Number(disk.used) || 0;
            const free = Math.max(0, total - used);
            const percent = total > 0 ? (used / total) * 100 : 0;
            const freePercent = total > 0 ? (free / total) * 100 : 0;

            let color = '#30d158';
            if (percent >= 85) color = '#ff453a';
            else if (percent >= 60) color = '#ff9f0a';

            let freeClass = 'disk-free';
            if (freePercent <= ALERT.diskFree.critical) freeClass += ' critical';
            else if (freePercent <= ALERT.diskFree.warn) freeClass += ' warn';

            let smartHtml = '';
            if (disk.health) {
                const healthClass = disk.health === 'PASSED' ? 'ok' :
                                   disk.health === 'FAILED' ? 'fail' : 'unknown';
                const healthLabel = disk.health === 'PASSED' ? t('disk.health.ok') :
                                   disk.health === 'FAILED' ? t('disk.health.fail') : t('disk.health.unknown');
                smartHtml += `<span class="disk-health ${cssClass(healthClass)}" title="S.M.A.R.T: ${escapeHtml(disk.health)}">${healthLabel}</span>`;
            }
            if (Number(disk.temp) > 0) {
                smartHtml += `<span class="disk-temp">${Math.round(disk.temp)}℃</span>`;
            }

            html += `
                <div class="disk-item">
                    <div class="disk-header">
                        <div class="disk-path">${escapeHtml(disk.path || '-')}</div>
                        <div class="disk-meta">${smartHtml}</div>
                    </div>
                    <div class="disk-bar">
                        <div class="disk-bar-fill" style="width:${Math.min(percent, 100)}%;background:${color};"></div>
                    </div>
                    <div class="disk-usage">${formatStorage(used)} / ${formatStorage(total)}</div>
                    <div class="disk-free-row">
                        <span class="${cssClass(freeClass)}" title="Free: ${freePercent.toFixed(1)}%">${t('disk.free', formatStorage(free))}</span>
                    </div>
                </div>
            `;
        });

        elements.diskList.innerHTML = html;
    }

    // ============================================================
    // ★ ストレージ詳細モーダル（CrystalDiskInfo 風）
    // ============================================================
    let storageList_ = [];

    function healthInfo(health) {
        const h = String(health || '').toUpperCase();
        if (h === 'PASSED') return { cls: 'ok', label: t('disk.health.ok') };
        if (h === 'FAILED') return { cls: 'fail', label: t('disk.health.fail') };
        return { cls: 'unknown', label: t('disk.health.unknown') };
    }

    function buildStorageCard(disk) {
        const total = Number(disk.total) || 0;
        const used = Number(disk.used) || 0;
        const free = Math.max(0, total - used);
        const percent = total > 0 ? (used / total) * 100 : 0;
        const hi = healthInfo(disk.health);

        let barColor = '#30d158';
        if (percent >= 85) barColor = '#ff453a';
        else if (percent >= 60) barColor = '#ff9f0a';

        const na = escapeHtml(t('storage.not_available'));
        const model = disk.model ? escapeHtml(disk.model) : na;
        const serial = disk.serial ? escapeHtml(disk.serial) : na;
        const temp = Number(disk.temp) > 0 ? Math.round(disk.temp) + '℃' : na;
        const write = Number(disk.write_bytes) > 0 ? formatBytes(disk.write_bytes) : na;
        const powerOn = Number(disk.power_on_hours) > 0
            ? t('storage.power_on_hours', Number(disk.power_on_hours))
            : na;

        let kind = '';
        const rot = Number(disk.rotation_rate) || 0;
        if (rot > 0) kind = t('storage.hdd', rot);
        else if (disk.model || disk.health) kind = t('storage.ssd');

        return `
            <div class="storage-card">
                <div class="storage-card-head">
                    <span class="storage-path" title="${escapeHtml(disk.path || '-')}">${escapeHtml(disk.path || '-')}</span>
                    <span class="disk-health ${cssClass(hi.cls)}">${escapeHtml(hi.label)}</span>
                </div>
                <div class="storage-grid">
                    <div class="storage-item"><span class="label">${t('storage.col_model')}</span><span class="value" title="${model}">${model}</span></div>
                    <div class="storage-item"><span class="label">${t('storage.col_serial')}</span><span class="value">${serial}</span></div>
                    <div class="storage-item"><span class="label">${t('storage.col_temp')}</span><span class="value">${temp}</span></div>
                    <div class="storage-item"><span class="label">${t('storage.col_write')}</span><span class="value">${write}</span></div>
                    <div class="storage-item"><span class="label">${t('storage.col_power_on')}</span><span class="value">${powerOn}</span></div>
                    <div class="storage-item"><span class="label">${t('storage.col_capacity')}</span><span class="value">${formatStorage(used)} / ${formatStorage(total)}</span></div>
                </div>
                <div class="disk-bar"><div class="disk-bar-fill" style="width:${Math.min(percent, 100)}%;background:${barColor};"></div></div>
                <div class="storage-card-foot">
                    <span>${t('storage.col_usage')}: ${percent.toFixed(1)}%</span>
                    <span>${t('disk.free', formatStorage(free))}</span>
                    ${kind ? `<span class="storage-kind">${escapeHtml(kind)}</span>` : ''}
                </div>
            </div>
        `;
    }

    function renderStorageModal() {
        if (!elements.storageList) return;
        const list = Array.isArray(storageList_) ? storageList_ : [];
        if (list.length === 0) {
            elements.storageList.innerHTML = `<div class="storage-empty">${escapeHtml(t('storage.none'))}</div>`;
            return;
        }
        elements.storageList.innerHTML = list.map(buildStorageCard).join('');

        // smartctl 未検出（健康状態も型番も無い）ときはヒントを出す。
        const hasSmart = list.some(d => d.health || d.model || Number(d.temp) > 0);
        if (elements.storageHint) {
            elements.storageHint.hidden = hasSmart;
            elements.storageHint.textContent = t('storage.smart_unavailable');
        }
    }

    function openStorageModal() {
        if (!elements.storageModal) return;
        renderStorageModal();
        elements.storageModal.style.display = 'flex';
    }

    function closeStorageModal() {
        if (!elements.storageModal) return;
        elements.storageModal.style.display = 'none';
    }

    if (elements.storageModalClose) {
        elements.storageModalClose.addEventListener('click', closeStorageModal);
    }
    if (elements.storageModal) {
        elements.storageModal.addEventListener('click', (e) => {
            if (e.target === elements.storageModal) closeStorageModal();
        });
    }
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') closeStorageModal();
    });

    // ストレージカードのクリック／キー操作で詳細を開く。
    function setupStorageCardClick() {
        const card = elements.diskCard;
        if (!card) return;
        const handler = () => {
            // Same guard as the process cards: a public viewer must not open
            // the S.M.A.R.T storage details (model / serial / written bytes).
            if (card.dataset.click !== 'storage') return;
            openStorageModal();
        };
        card.addEventListener('click', handler);
        card.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                handler();
            }
        });
    }

    // ============================================================
    // ★ Phase 6: プロセスモーダル
    // ============================================================
    function openProcessModal(sortKey) {
        if (!elements.processModal) return;
        processState.sortKey = sortKey || 'cpu';
        processState.isOpen = true;

        // ソートボタンの見た目を更新
        document.querySelectorAll('.sort-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.sort === processState.sortKey);
        });

        // モーダル表示
        elements.processModal.style.display = 'flex';

        // タイトル更新
        if (elements.processModalTitle) {
            elements.processModalTitle.textContent = processState.sortKey === 'mem' ? t('process.title_mem') : t('process.title_cpu');
        }
        if (elements.processHint) {
            elements.processHint.textContent = processState.sortKey === 'mem' ? t('process.hint_mem') : t('process.hint_cpu');
        }

        renderProcessTable();

        // 検索欄にフォーカス
        setTimeout(() => elements.processSearch?.focus(), 50);
    }

    function closeProcessModal() {
        if (!elements.processModal) return;
        elements.processModal.style.display = 'none';
        processState.isOpen = false;
        processState.search = '';
        if (elements.processSearch) elements.processSearch.value = '';
    }

    function renderProcessTable() {
        if (!elements.processTableBody) return;

        let list = processState.list || [];

        // ソート
        const key = processState.sortKey === 'mem' ? 'mem_mb' : 'cpu';
        list = [...list].sort((a, b) => {
            const av = Number(a[key]) || 0;
            const bv = Number(b[key]) || 0;
            return bv - av;
        });

        // 検索フィルタ
        const q = processState.search.trim().toLowerCase();
        if (q) {
            list = list.filter(p =>
                String(p.name || '').toLowerCase().includes(q) ||
                String(p.pid).includes(q) ||
                String(p.user || '').toLowerCase().includes(q)
            );
        }

        if (list.length === 0) {
            elements.processTableBody.innerHTML = `
                <tr><td colspan="5" class="process-empty">
                    ${processState.search ? t('process.empty_search') : t('process.empty_waiting')}
                </td></tr>
            `;
            return;
        }

        let html = '';
        list.forEach(p => {
            const cpu = Number(p.cpu) || 0;
            const memMB = Number(p.mem_mb) || 0;

            let cpuClass = '';
            if (cpu >= 80) cpuClass = 'process-cpu-high';
            else if (cpu >= 50) cpuClass = 'process-cpu-mid';

            html += `
                <tr>
                    <td class="col-pid">${escapeHtml(p.pid)}</td>
                    <td class="col-name" title="${escapeHtml(p.name)}">${escapeHtml(p.name)}</td>
                    <td class="col-cpu ${cssClass(cpuClass)}">${cpu.toFixed(1)}%</td>
                    <td class="col-mem">${formatMemMB(memMB)}</td>
                    <td class="col-user" title="${escapeHtml(p.user || '')}">${escapeHtml(p.user || '-')}</td>
                </tr>
            `;
        });
        elements.processTableBody.innerHTML = html;
    }

    // カードクリック処理
    function setupProcessCardClicks() {
        document.querySelectorAll('[data-click="process"]').forEach(card => {
            const handler = () => {
                // Re-check the attribute at click time: applyAccessMode removes
                // data-click for public viewers, but the listener was attached
                // earlier and would otherwise still open the modal.
                if (card.dataset.click !== 'process') return;
                const sort = card.dataset.processSort || 'cpu';
                openProcessModal(sort);
            };
            card.addEventListener('click', handler);
            card.addEventListener('keydown', (e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    handler();
                }
            });
        });
    }

    // モーダルイベント
    if (elements.processModalClose) {
        elements.processModalClose.addEventListener('click', closeProcessModal);
    }
    if (elements.processModal) {
        elements.processModal.addEventListener('click', (e) => {
            if (e.target === elements.processModal) closeProcessModal();
        });
    }
    if (elements.processSearch) {
        elements.processSearch.addEventListener('input', (e) => {
            processState.search = e.target.value;
            renderProcessTable();
        });
    }
    document.querySelectorAll('.sort-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            processState.sortKey = btn.dataset.sort || 'cpu';
            document.querySelectorAll('.sort-btn').forEach(b => {
                b.classList.toggle('active', b === btn);
            });
            if (elements.processModalTitle) {
                elements.processModalTitle.textContent = processState.sortKey === 'mem' ? t('process.title_mem') : t('process.title_cpu');
            }
            if (elements.processHint) {
                elements.processHint.textContent = processState.sortKey === 'mem' ? t('process.hint_mem') : t('process.hint_cpu');
            }
            renderProcessTable();
        });
    });
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && processState.isOpen) {
            closeProcessModal();
        }
    });

    // ============================================================
    // バックアップ
    // ============================================================

    // 現在のバックアップ状態（最新の実行結果）。プラグイン一覧の表示にも使う。
    let currentBackup = {};
    // 直近取得したモジュール一覧。バックアップ状態変化時に再描画するため保持。
    let lastModules = [];
    // プラグインメタ（name → meta）。表示名や種別判定に使う。
    let pluginMetaMap = new Map();

    // 実行ステータスを「ラベル・色クラス」に正規化する。
    function normalizeStatus(status) {
        const s = String(status || '').toLowerCase();
        if (s === 'success' || s === 'completed' || s === 'ok') {
            return { key: 'success', label: t('status.success'), cls: 'success' };
        }
        if (s === 'running' || s === 'processing') {
            return { key: 'running', label: t('status.running'), cls: 'running' };
        }
        if (s === 'failed' || s === 'error') {
            return { key: 'failed', label: t('status.failed'), cls: 'failed' };
        }
        if (s === '' || s === 'unknown' || s === 'standby') {
            return { key: 'standby', label: t('plugin.standby'), cls: 'unknown' };
        }
        return { key: s, label: status, cls: 'unknown' };
    }

    // ISO 文字列を「MM/DD HH:mm:ss」に整形する。失敗時は元の文字列を返す。
    function formatTimestamp(value) {
        if (!value) return '';
        const d = new Date(value);
        if (isNaN(d.getTime())) return String(value);
        const mm = String(d.getMonth() + 1).padStart(2, '0');
        const dd = String(d.getDate()).padStart(2, '0');
        const hh = String(d.getHours()).padStart(2, '0');
        const mi = String(d.getMinutes()).padStart(2, '0');
        const ss = String(d.getSeconds()).padStart(2, '0');
        return `${mm}/${dd} ${hh}:${mi}:${ss}`;
    }

    function updateBackup(backup) {
        const section = elements.backupSection;
        if (!section) return;

        // Guard against a missing backup object.
        if (!backup || typeof backup !== 'object') backup = {};
        currentBackup = backup;

        // Always show the plugin status section.
        section.hidden = false;

        const norm = normalizeStatus(backup.status);

        // Status badge (single source of truth for label + color).
        if (elements.backupStatusBadge) {
            const badge = elements.backupStatusBadge;
            badge.className = 'backup-status-badge';
            badge.textContent = norm.label;
            badge.classList.add(norm.cls);
        }

        // 実行状態が変わったのでプラグイン一覧も更新する。
        renderPluginList(lastModules);
    }

    // ============================================================
    // プラグイン一覧
    // ============================================================
    // モジュール1件分のカードHTMLを組み立てる。
    function buildPluginCard(mod) {
        const enabled = mod.enabled !== false;
        const type = (mod.type || 'plugin').toLowerCase();
        const meta = pluginMetaMap.get(mod.name) || {};
        const displayName = meta.display_name || mod.name;
        const isBackup = meta.is_backup === true;
        const isActive = currentBackup && currentBackup.name === mod.name;
        const activeNorm = normalizeStatus(currentBackup ? currentBackup.status : '');

        // 状態バッジ。
        //   無効 → 無効
        //   監視系（セキュリティ等）で有効 → 監視中（常時動作）
        //   バックアップ系で実行中 → 実行中/成功/失敗
        //   バックアップ系で待機 → 待機中
        let stateCls;
        let stateLabel;
        if (!enabled) {
            stateCls = 'disabled';
            stateLabel = t('plugin.disabled');
        } else if (!isBackup) {
            stateCls = 'success';
            stateLabel = t('security.state_active');
        } else if (isActive) {
            stateCls = activeNorm.cls;
            stateLabel = activeNorm.label;
        } else {
            stateCls = 'standby';
            stateLabel = t('plugin.standby');
        }

        const cfg = mod.config || {};

        // 本文の項目。バックアップ系と監視系で表示を分ける。
        let body;
        if (isBackup) {
            const lastRun = (isActive && currentBackup.last_run)
                ? formatTimestamp(currentBackup.last_run)
                : (cfg.last_run ? formatTimestamp(cfg.last_run) : t('plugin.not_run'));
            const nextRun = (isActive && currentBackup.next_run)
                ? formatTimestamp(currentBackup.next_run)
                : '-'
            const size = (isActive && Number(currentBackup.size) > 0)
                ? formatBytes(currentBackup.size)
                : '-'
            body = `
                <div class="plugin-card-row">
                    <span class="label">${t('plugin.last_run')}</span>
                    <span class="value">${escapeHtml(lastRun)}</span>
                </div>
                <div class="plugin-card-row">
                    <span class="label">${t('plugin.next_run')}</span>
                    <span class="value">${escapeHtml(nextRun)}</span>
                </div>
                <div class="plugin-card-row">
                    <span class="label">${t('modules.label_size')}</span>
                    <span class="value">${escapeHtml(size)}</span>
                </div>
            `;
        } else {
            const watchFiles = cfg.watch_files || '-';
            const notifyLevel = cfg.notify_min_level
                ? t('level.' + String(cfg.notify_min_level).toLowerCase())
                : '-';
            const burst = cfg.failed_burst
                ? t('security.burst_value', cfg.failed_burst, cfg.burst_window_sec || '-')
                : '-';
            const lastDetect = (isActive && currentBackup.last_run)
                ? formatTimestamp(currentBackup.last_run)
                : '-';
            body = `
                <div class="plugin-card-row">
                    <span class="label">${t('security.label_last_detect')}</span>
                    <span class="value">${escapeHtml(lastDetect)}</span>
                </div>
                <div class="plugin-card-row">
                    <span class="label">${t('security.label_watch')}</span>
                    <span class="value wrap" title="${escapeHtml(watchFiles)}">${escapeHtml(watchFiles)}</span>
                </div>
                <div class="plugin-card-row">
                    <span class="label">${t('security.label_notify')}</span>
                    <span class="value">${escapeHtml(notifyLevel)}</span>
                </div>
                <div class="plugin-card-row">
                    <span class="label">${t('security.label_burst')}</span>
                    <span class="value">${escapeHtml(burst)}</span>
                </div>
            `;
        }

        // カード全体をリンクにする。プラグイン専用 Web UI があればそこへ、
        // 無ければモジュール管理画面（操作・設定ができる）へ遷移する。
        const href = (meta.has_web_ui)
            ? `/plugins/${encodeURIComponent(mod.name)}/`
            : '/modules.html';
        const target = meta.has_web_ui ? ' target="_blank" rel="noopener"' : '';

        return `
            <a class="plugin-card ${enabled ? 'enabled' : 'disabled'} ${isActive ? 'active' : ''}" href="${escapeAttr(href)}"${target}>
                <div class="plugin-card-header">
                    <span class="plugin-card-name" title="${escapeHtml(displayName)}">${escapeHtml(displayName)}</span>
                    <span class="plugin-row-type">${escapeHtml(type)}</span>
                    <span class="plugin-row-state ${cssClass(stateCls)}">${escapeHtml(stateLabel)}</span>
                </div>
                <div class="plugin-card-body">
                    ${body}
                </div>
            </a>
        `;
    }

    function renderPluginList(modules) {
        if (!elements.pluginList) return;

        const list = Array.isArray(modules) ? modules : [];
        lastModules = list;
        if (elements.moduleCount) elements.moduleCount.textContent = String(list.length);

        if (list.length === 0) {
            elements.pluginList.innerHTML = `<div class="plugin-list-empty">${escapeHtml(t('plugin.none'))}</div>`;
            return;
        }

        elements.pluginList.innerHTML = list.map(buildPluginCard).join('');
    }

    async function loadPluginList() {
        try {
            const [res, metaRes] = await Promise.all([
                fetch('/api/modules'),
                fetch('/api/plugins').catch(() => null),
            ]);
            if (res && res.ok) {
                const data = await res.json();
                renderPluginList(data);
            }
            // プラグインメタ（表示名・is_backup 判定用）を取得して再描画する。
            if (metaRes && metaRes.ok) {
                const metaData = await metaRes.json();
                pluginMetaMap = new Map();
                (metaData.plugins || []).forEach(m => pluginMetaMap.set(m.name, m));
                renderPluginList(lastModules);
            }
        } catch (e) {
            // Non-fatal; ignore.
        }
    }

    // ============================================================
    // ダッシュボード更新
    // ============================================================
    function updateDashboard(data) {
        if (!data) return;

        // ---------- CPU ----------
        const cpu = clamp(data.cpu_usage ?? data.cpu_percent ?? 0, 0, 100);
        if (elements.cpuValue) elements.cpuValue.textContent = `${cpu.toFixed(1)}%`;
        if (elements.cpuDetail) elements.cpuDetail.textContent = t('card.usage', cpu.toFixed(1));
        drawGauge(elements.cpuGauge, cpu, 'cpu');
        updateCpuCores(data.cpu_per_core ?? [], data.cpu_freq ?? data.cpu_frequency ?? 0);

        if (elements.cpuModel) {
            elements.cpuModel.textContent = data.cpu_model || '-';
        }
        const cpuTemp = data.cpu_temp ?? 0;
        updateTempBadge(elements.cpuTempBadge, cpuTemp);

        // ---------- メモリ ----------
        const memoryPercent = clamp(data.mem_percent ?? 0, 0, 100);
        const memoryUsed = data.mem_used ?? 0;
        const memoryTotal = data.mem_total ?? 0;

        if (elements.memValue) elements.memValue.textContent = `${memoryPercent.toFixed(1)}%`;
        if (elements.memDetail) {
            elements.memDetail.textContent = `${formatBytesFixed(memoryUsed, 'GB')} / ${formatBytesFixed(memoryTotal, 'GB')}`;
        }
        // Memory thresholds are configured as *usage* percentages; convert
        // them to free-space percentages for the badge.
        updateFreeBadge(elements.memFreeBadge, memoryTotal - memoryUsed, memoryTotal,
            100 - ALERT.mem.critical, 100 - ALERT.mem.warn);
        drawGauge(elements.memGauge, memoryPercent, 'mem');

        // ---------- ストレージ ----------
        const disks = data.disks ?? [];
        let diskPercent = data.disk_percent ?? 0;
        let diskUsed = data.disk_used ?? 0;
        let diskTotal = data.disk_total ?? 0;

        if (Array.isArray(disks) && disks.length > 0) {
            let used = 0, total = 0;
            disks.forEach(d => { used += Number(d.used) || 0; total += Number(d.total) || 0; });
            diskUsed = used; diskTotal = total;
            diskPercent = total > 0 ? (used / total) * 100 : 0;
        }

        diskPercent = clamp(diskPercent, 0, 100);
        if (elements.diskValue) elements.diskValue.textContent = `${diskPercent.toFixed(1)}%`;
        if (elements.diskDetail) {
            elements.diskDetail.textContent = `${formatStorage(diskUsed)} / ${formatStorage(diskTotal)}`;
        }
        updateFreeBadge(elements.diskFreeBadge, diskTotal - diskUsed, diskTotal,
            ALERT.diskFree.critical, ALERT.diskFree.warn);
        drawGauge(elements.diskGauge, diskPercent, 'disk');
        updateDiskList(disks);
        storageList_ = Array.isArray(disks) ? disks : [];
        if (elements.storageModal && elements.storageModal.style.display === 'flex') {
            renderStorageModal();
        }

        let maxDiskTemp = 0;
        if (Array.isArray(disks)) {
            disks.forEach(d => {
                const tempVal = Number(d.temp) || 0;
                if (tempVal > maxDiskTemp) maxDiskTemp = tempVal;
            });
        }
        updateTempBadge(elements.diskTempBadge, maxDiskTemp);

        // ---------- 稼働時間 ----------
        const uptime = data.uptime ?? 0;
        const uptimeText = formatUptime(uptime);
        if (elements.uptimeValue) elements.uptimeValue.textContent = uptimeText.value;

        if (elements.bootTime) {
            elements.bootTime.textContent = formatBootTime(uptime);
        }

        if (elements.loadAvg) {
            const la = data.load_average;
            if (Array.isArray(la) && la.length >= 3) {
                elements.loadAvg.textContent = `${Number(la[0]).toFixed(2)} / ${Number(la[1]).toFixed(2)} / ${Number(la[2]).toFixed(2)}`;
            } else {
                elements.loadAvg.textContent = '-- / -- / --';
            }
        }

        if (elements.networkIO) {
            const speed = data.network_speed;
            if (speed && (speed.up || speed.down)) {
                elements.networkIO.textContent = `↓ ${formatSpeed(speed.down)} / ↑ ${formatSpeed(speed.up)}`;
            } else {
                elements.networkIO.textContent = '↓ 0 B/s / ↑ 0 B/s';
            }
        }

        // ---------- ★ Phase 6: プロセス一覧を保持 ----------
        if (Array.isArray(data.processes)) {
            processState.list = data.processes;
            if (processState.isOpen) {
                renderProcessTable();
            }
        }

        // ---------- バックアップ ----------
        updateBackup(data.backup);

        // ---------- アラート ----------
        const diskFreePercent = 100 - diskPercent;
        checkAlerts(cpu, cpuTemp, memoryPercent, diskFreePercent);
    }

    // ============================================================
    // 現在時刻クロック
    // ============================================================
    function startClock() {
        if (!elements.uptimeDetail) return;
        const tick = () => {
            elements.uptimeDetail.textContent = t('uptime.current_time', formatClock(new Date()));
        };
        tick();
        setInterval(tick, 1000);
    }

    // ============================================================
    // WebSocket
    // ============================================================
    let socket = null;
    let reconnectTimer = null;
    let reconnectAttempts = 0;
    // 切断トーストを一度だけ出すためのフラグ（多重表示防止）。
    let disconnectNotified = false;
    const RECONNECT_DELAY = 3000;

    function connectWebSocket() {
        if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
            return;
        }

        setConnectionState('connecting');

        const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const url = `${protocol}//${location.host}/ws`;

        try {
            socket = new WebSocket(url);
        } catch (error) {
            console.error('[Kizuna-Eye] WebSocket:', error);
            setConnectionState('disconnected');
            scheduleReconnect();
            return;
        }

        socket.addEventListener('open', () => {
            console.log('[Kizuna-Eye] WebSocket connected');
            reconnectAttempts = 0;
            // 一度切断を通知した後に復帰したら「再接続しました」を出す。
            if (disconnectNotified) {
                disconnectNotified = false;
                showToast(t('toast.reconnected'), 'success');
            }
            setConnectionState('connected');
        });

        socket.addEventListener('message', event => {
            try {
                const data = JSON.parse(event.data);
                if (data.action) return;
                if (data.event === 'backup_result') {
                    // Reflect the manual backup result immediately.
                    // 既存の状態（プラグイン名・次回実行）を保持するためマージする。
                    updateBackup(Object.assign({}, currentBackup, {
                        name: currentBackup.name || data.plugin,
                        status: data.status,
                        size: data.size,
                        last_run: data.timestamp,
                    }));
                    return;
                }
                if (data.event) return;
                window._lastData = data;
                updateDashboard(data);
            } catch (error) {
                console.error('[Kizuna-Eye] JSON:', error);
            }
        });

        socket.addEventListener('close', () => {
            setConnectionState('disconnected');
            // 切断を一度だけポップアップで知らせる。再接続を繰り返す間に
            // 何度も出ると煩わしいので、復帰するまで再表示しない。
            if (!disconnectNotified) {
                disconnectNotified = true;
                showToast(t('toast.disconnected'), 'error');
            }
            scheduleReconnect();
        });

        socket.addEventListener('error', error => {
            console.warn('[Kizuna-Eye] WebSocket error:', error);
        });
    }

    function scheduleReconnect() {
        if (reconnectTimer) return;
        reconnectAttempts++;
        const delay = Math.min(RECONNECT_DELAY * reconnectAttempts, 15000);
        reconnectTimer = setTimeout(() => {
            reconnectTimer = null;
            connectWebSocket();
        }, delay);
    }

    // ============================================================
    // バックアップ折りたたみ
    // ============================================================
    let backupCollapsed = false;

    function toggleBackup() {
        backupCollapsed = !backupCollapsed;
        if (elements.backupBody) {
            elements.backupBody.classList.toggle('collapsed', backupCollapsed);
        }
        if (elements.backupToggle) {
            elements.backupToggle.setAttribute('aria-expanded', String(!backupCollapsed));
            // Scope the arrow lookup to THIS section. A global querySelector
            // returned the history section's button (first in DOM order), so
            // the plugin section arrow never rotated.
            const btn = elements.backupToggle.querySelector('.backup-toggle-btn');
            if (btn) btn.classList.toggle('collapsed', backupCollapsed);
        }
    }

    if (elements.backupToggle) {
        elements.backupToggle.addEventListener('click', toggleBackup);
    }

    // ============================================================
    // メトリクス履歴（簡易トレンド）
    // ============================================================
    let historyCollapsed = false;

    function toggleHistory() {
        historyCollapsed = !historyCollapsed;
        if (elements.historyBody) elements.historyBody.classList.toggle('collapsed', historyCollapsed);
        if (elements.historyToggle) {
            elements.historyToggle.setAttribute('aria-expanded', String(!historyCollapsed));
            const icon = elements.historyToggle.querySelector('.backup-toggle-btn');
            if (icon) icon.classList.toggle('collapsed', historyCollapsed);
        }
    }

    if (elements.historyToggle) {
        elements.historyToggle.addEventListener('click', toggleHistory);
    }

    async function loadHistory() {
        try {
            const res = await fetch('/api/history');
            if (!res.ok) return;
            const data = await res.json();
            drawHistoryChart(data.samples || []);
            if (elements.historyRangeBadge) {
                elements.historyRangeBadge.textContent = String((data.samples || []).length);
            }
        } catch (e) {
            // 履歴取得失敗は致命的ではないので無視
        }
    }

    function drawHistoryChart(samples) {
        const canvas = elements.historyChart;
        if (!canvas) return;
        const ctx = canvas.getContext('2d');
        if (!ctx) return;

        // Match the canvas backing store to its CSS size for sharp rendering.
        const cssW = canvas.clientWidth || 600;
        const cssH = 220;
        if (canvas.width !== cssW || canvas.height !== cssH) {
            canvas.width = cssW;
            canvas.height = cssH;
        }
        const w = canvas.width;
        const h = canvas.height;
        ctx.clearRect(0, 0, w, h);

        // Grid + axis labels (0/50/100%).
        ctx.strokeStyle = 'rgba(150,150,170,0.15)';
        ctx.fillStyle = 'rgba(160,170,195,0.7)';
        ctx.font = '10px monospace';
        ctx.lineWidth = 1;
        [0, 50, 100].forEach(pct => {
            const y = h - (pct / 100) * (h - 10) - 5;
            ctx.beginPath();
            ctx.moveTo(28, y);
            ctx.lineTo(w - 5, y);
            ctx.stroke();
            ctx.fillText(pct + '%', 2, y + 3);
        });

        if (!Array.isArray(samples) || samples.length < 2) {
            ctx.fillStyle = 'rgba(160,170,195,0.7)';
            ctx.font = '12px sans-serif';
            ctx.fillText(t('history.waiting'), 40, h / 2);
            return;
        }

        const x0 = 28;
        const x1 = w - 5;
        const plotW = x1 - x0;
        const plotH = h - 10;

        const series = [
            { key: 'cpu', color: '#18d66b' },
            { key: 'mem', color: '#7b3ff2' },
            { key: 'disk', color: '#d946ef' },
        ];

        series.forEach(s => {
            ctx.beginPath();
            ctx.strokeStyle = s.color;
            ctx.lineWidth = 1.5;
            samples.forEach((p, i) => {
                const x = x0 + (i / (samples.length - 1)) * plotW;
                const val = clamp(p[s.key] || 0, 0, 100);
                const y = h - 5 - (val / 100) * plotH;
                if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
            });
            ctx.stroke();
        });
    }

    // ============================================================
    // アラート履歴
    // ============================================================
    let alertsCollapsed = false;

    function toggleAlerts() {
        alertsCollapsed = !alertsCollapsed;
        if (elements.alertsBody) elements.alertsBody.classList.toggle('collapsed', alertsCollapsed);
        if (elements.alertsToggle) {
            elements.alertsToggle.setAttribute('aria-expanded', String(!alertsCollapsed));
            const icon = elements.alertsToggle.querySelector('.backup-toggle-btn');
            if (icon) icon.classList.toggle('collapsed', alertsCollapsed);
        }
    }

    if (elements.alertsToggle) {
        elements.alertsToggle.addEventListener('click', toggleAlerts);
    }

    async function clearAlerts() {
        if (!window.confirm(t('alerts.clear_confirm'))) return;
        try {
            const res = await fetch('/api/alerts', { method: 'DELETE' });
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            renderAlerts([]);
            showToast(t('alerts.cleared'), 'success');
        } catch (e) {
            showToast(t('alerts.clear_failed'), 'error');
        }
    }

    if (elements.alertsClearBtn) {
        elements.alertsClearBtn.addEventListener('click', (e) => {
            // ヘッダー全体の折りたたみトグルに伝播させない
            e.stopPropagation();
            clearAlerts();
        });
    }

    function renderAlerts(alerts) {
        if (!elements.alertList) return;
        if (!Array.isArray(alerts) || alerts.length === 0) {
            elements.alertList.innerHTML = `<div class="alert-empty">${escapeHtml(t('alerts.empty'))}</div>`;
            if (elements.alertsCountBadge) elements.alertsCountBadge.textContent = '0';
            return;
        }
        if (elements.alertsCountBadge) elements.alertsCountBadge.textContent = String(alerts.length);

        let html = '';
        const knownLevels = ['info', 'warning', 'critical', 'success'];
        alerts.forEach(a => {
            const rawLevel = String(a.level || 'info').toLowerCase();
            const level = knownLevels.indexOf(rawLevel) >= 0 ? rawLevel : 'info';
            const levelLabel = t('level.' + level);
            const time = a.timestamp ? new Date(a.timestamp).toLocaleString() : '';
            // Source badge: trusted (dashboard-generated) vs untrusted (agent/log-derived).
            let srcBadge = '';
            if (a.source) {
                const trusted = a.trusted === true;
                const cls = trusted ? 'trusted' : 'untrusted';
                const label = trusted ? t('alerts.source_trusted') : t('alerts.source_untrusted');
                srcBadge = `<span class="alert-source-badge ${cssClass(cls)}" title="${escapeHtml(a.source)}">${escapeHtml(label)}</span>`;
            }
            const idAttr = a.id ? ` data-alert-id="${escapeHtml(a.id)}"` : '';
            const clickable = a.id ? ' clickable' : '';
            html += `
                <div class="alert-item ${cssClass(level)}${clickable}"${idAttr}>
                    <span class="alert-level-badge ${cssClass(level)}">${escapeHtml(levelLabel)}</span>
                    ${srcBadge}
                    <div class="alert-item-body">
                        <div class="alert-item-title">${escapeHtml(pickLocalized(a.title, a.title_en))}</div>
                        <div class="alert-item-message">${escapeHtml(pickLocalized(a.message, a.message_en))}</div>
                    </div>
                    <span class="alert-item-time">${escapeHtml(time)}</span>
                </div>
            `;
        });
        elements.alertList.innerHTML = html;
        // クリックで詳細モーダルを開く (タスク1)。
        Array.from(elements.alertList.querySelectorAll('.alert-item[data-alert-id]')).forEach(el => {
            el.addEventListener('click', () => showAlertDetail(el.getAttribute('data-alert-id')));
        });
    }

    // ============================================================
    // アラート詳細モーダル (タスク1)
    // ============================================================
    async function showAlertDetail(id) {
        if (!id) return;
        let a;
        try {
            const res = await fetch(`/api/alerts/${encodeURIComponent(id)}`);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            a = await res.json();
        } catch (e) {
            showToast(t('alerts.detail_failed'), 'error');
            return;
        }
        const level = String(a.level || 'info').toLowerCase();
        const time = a.timestamp ? new Date(a.timestamp).toLocaleString() : '';
        const row = (label, value) => value
            ? `<div class="alert-detail-row"><span class="alert-detail-label">${escapeHtml(label)}</span><span class="alert-detail-value">${escapeHtml(value)}</span></div>`
            : '';
        const detectLogic = a.detect_file
            ? `${a.detect_file}${a.detect_line ? ':' + a.detect_line : ''}`
            : '';
        // 対処ボタン (タスク2)。アラート種別に応じて出し分ける。
        const actions = suggestActions(a);
        const actionsHtml = actions.length ? `
            <div class="alert-detail-actions">
                <div class="alert-detail-actions-title">${escapeHtml(t('alerts.d_actions'))}</div>
                ${actions.map(act => `<button type="button" class="btn-secondary alert-action-btn" data-action="${escapeHtml(act.action)}" data-target="${escapeHtml(act.target || '')}">${escapeHtml(act.label)}</button>`).join('')}
            </div>
        ` : '';
        const html = `
            <div class="alert-detail">
                <div class="alert-detail-head">
                    <span class="alert-level-badge ${cssClass(level)}">${escapeHtml(t('level.' + level))}</span>
                    <span class="alert-detail-title">${escapeHtml(pickLocalized(a.title, a.title_en))}</span>
                </div>
                <div class="alert-detail-row"><span class="alert-detail-label">${escapeHtml(t('alerts.d_time'))}</span><span class="alert-detail-value">${escapeHtml(time)}</span></div>
                ${row(t('alerts.d_message'), pickLocalized(a.message, a.message_en))}
                ${row(t('alerts.d_command'), a.command)}
                ${row(t('alerts.d_detect'), detectLogic)}
                ${row(t('alerts.d_remediation'), a.remediation)}
                ${row(t('alerts.d_type'), a.type)}
                ${row(t('alerts.d_source'), a.source)}
                ${a.related_log ? `<div class="alert-detail-row"><span class="alert-detail-label">${escapeHtml(t('alerts.d_related'))}</span><pre class="alert-detail-log">${escapeHtml(a.related_log)}</pre></div>` : ''}
                ${(a.command || a.detect_file || a.remediation || a.related_log) ? '' : `<div class="alert-detail-note">${escapeHtml(t('alerts.d_no_detail'))}</div>`}
                ${actionsHtml}
            </div>
        `;
        openModal(t('alerts.d_title'), html);
        // 対処ボタンに click を配線する。
        Array.from(document.querySelectorAll('#alertDetailModal .alert-action-btn')).forEach(btn => {
            btn.addEventListener('click', () => runAlertAction(a.id, btn.getAttribute('data-action'), btn.getAttribute('data-target')));
        });
    }

    // extractPortFromAlert pulls the TCP port from a listening-port alert's
    // command ("待ち受け: TCP 0.0.0.0:55555") or message ("ポート 55555 が...").
    function extractPortFromAlert(a) {
        const cmd = String(a.command || '');
        let m = cmd.match(/:(\d{1,5})\b/);
        if (m) return m[1];
        const msg = String(a.message || '');
        m = msg.match(/ポート\s*(\d{1,5})/);
        if (m) return m[1];
        return '';
    }

    // suggestActions returns the buttons appropriate for an alert.
    // It is intentionally conservative: only actions with a sensible default
    // target (or that can prompt the operator) are offered.
    function suggestActions(a) {
        const out = [];
        const title = String(a.title || '');
        const type = String(a.type || '');
        const ipMatch = String(a.message || '').match(/\b(?:\d{1,3}\.){3}\d{1,3}\b/);
        const ip = ipMatch ? ipMatch[0] : '';
        if (type === 'security_ssh_failed' || type === 'security_ssh_login') {
            if (ip) out.push({ action: 'block_ip', label: t('alerts.a_block_ip') + ' (' + ip + ')', target: ip });
        }
        if (type === 'security_listen_port') {
            // モーダルはポートを対象と分かっているので、PID を手入力させず
            // ポートをそのまま渡す。ヘルパー側が ss でPIDを解決して停止する。
            const port = extractPortFromAlert(a);
            if (port) {
                out.push({ action: 'kill_port', label: t('alerts.a_kill_port') + ' (:' + port + ')', target: port });
            } else {
                // ポートが取れない異例のケースだけ、従来の PID 手入力へ。
                out.push({ action: 'kill_process', label: t('alerts.a_kill_process'), target: '' });
            }
        }
        if (type === 'security_cron') {
            out.push({ action: 'delete_cron', label: t('alerts.a_delete_cron'), target: '' });
        }
        if (type === 'security_integrity' || type === 'security_suid') {
            out.push({ action: 'restore_file', label: t('alerts.a_restore_file'), target: a.source || '' });
        }
        return out;
    }

    // runAlertAction asks for confirmation (and a missing target, e.g. a PID)
    // then POSTs to the action API.
    async function runAlertAction(id, action, target) {
        if (!id || !action) return;
        // kill_process だけが PID の手入力を必要とする（kill_port は
        // ポートからヘルパーが PID を解決するので入力不要）。
        if (action === 'kill_process' && !target) {
            const pid = window.prompt(t('alerts.a_pid_prompt'), '');
            if (!pid) return;
            if (!/^[0-9]{1,10}$/.test(pid)) {
                showToast(t('alerts.a_pid_invalid'), 'error');
                return;
            }
            target = pid;
        }
        // restore_file はバックアップパスが必要。
        let backup = '';
        if (action === 'restore_file') {
            backup = window.prompt(t('alerts.a_backup_prompt'), '') || '';
            if (!backup) return;
        }
        const label = t('alerts.a_' + action);
        if (!window.confirm(t('alerts.a_confirm', label, target))) return;
        try {
            const res = await fetch(`/api/alerts/${encodeURIComponent(id)}/action`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ action: action, target: target, backup: backup })
            });
            const data = await res.json().catch(() => ({}));
            if (res.ok && data.status === 'success') {
                showToast(t('alerts.a_done'), 'success');
            } else {
                showToast(t('alerts.a_failed', data.detail || ('HTTP ' + res.status)), 'error');
            }
        } catch (e) {
            showToast(t('alerts.a_failed', e.message), 'error');
        }
        // 結果（監査エントリ含む）を反映するため履歴を再取得する。
        loadAlerts();
        const modal = document.getElementById('alertDetailModal');
        if (modal) modal.remove();
    }

    // openModal builds a simple overlay modal. contentHtml must already be
    // escaped by the caller.
    function openModal(title, contentHtml) {
        const existing = document.getElementById('alertDetailModal');
        if (existing) existing.remove();
        const overlay = document.createElement('div');
        overlay.id = 'alertDetailModal';
        overlay.className = 'modal-overlay';
        overlay.innerHTML = `
            <div class="modal-box" role="dialog" aria-modal="true">
                <div class="modal-head">
                    <h3>${escapeHtml(title)}</h3>
                    <button type="button" class="modal-close" aria-label="close">✕</button>
                </div>
                <div class="modal-body">${contentHtml}</div>
            </div>
        `;
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) overlay.remove();
        });
        overlay.querySelector('.modal-close').addEventListener('click', () => overlay.remove());
        document.body.appendChild(overlay);
    }

    async function loadAlerts() {
        try {
            const res = await fetch('/api/alerts');
            if (!res.ok) return;
            const data = await res.json();
            renderAlerts(data.alerts || []);
        } catch (e) {
            // 履歴取得失敗は致命的ではないので無視
        }
    }

    // ============================================================
    // 初期描画
    // ============================================================
    function drawInitialGauges() {
        drawGauge(elements.cpuGauge, 0, 'cpu');
        drawGauge(elements.memGauge, 0, 'mem');
        drawGauge(elements.diskGauge, 0, 'disk');
    }

    // ============================================================
    // カード並べ替え（ドラッグ & ドロップ）
    // 並び順は localStorage の kizuna-card-order に保存し、次回起動時に復元する。
    // マウスとタッチの両方に対応するため Pointer Events を使用する。
    // ============================================================
    const CARD_ORDER_KEY = 'kizuna-card-order';
    const SECTION_ORDER_KEY = 'kizuna-section-order';

    // 並べ替え対象。ゲージカード4枚は .gauge-grid 内、履歴・プラグイン・
    // アラートの3セクションは .main 直下で、それぞれ独立に並べ替えられる。
    const REORDER_CARD_SELECTOR = '.gauge-card';
    const REORDER_SECTION_SELECTOR = '.history-section, .backup-section, .alerts-section';

    function cardKey(card) {
        return card.id || card.className;
    }

    // orderKey はコンテナごとに別の保存キーを使う（カードとセクションの
    // 並び順が互いを上書きしないように）。main コンテナならセクション用。
    function orderKeyFor(container) {
        return container.classList.contains('main') ? SECTION_ORDER_KEY : CARD_ORDER_KEY;
    }

    function saveCardOrder(container) {
        try {
            const order = Array.from(container.children).map(cardKey);
            localStorage.setItem(orderKeyFor(container), JSON.stringify(order));
        } catch (e) { /* localStorage 不可の環境では無視 */ }
    }

    function applyCardOrder(container) {
        let order;
        try {
            order = JSON.parse(localStorage.getItem(orderKeyFor(container)) || '[]');
        } catch (e) { order = []; }
        if (!Array.isArray(order) || order.length === 0) return;

        const byKey = new Map();
        Array.from(container.children).forEach(card => byKey.set(cardKey(card), card));
        // Append in the saved order; anything not in the saved order keeps
        // its relative position at the end.
        order.forEach(key => {
            const card = byKey.get(key);
            if (card) container.appendChild(card);
        });
    }

    // reorderContainer は、その直下の子を並べ替え対象にするコンテナ。
    // selector に一致する子だけが並べ替え対象になる。
    // カードは .gauge-grid（selector=.gauge-card）、
    // セクションは .main（selector=セクション3種）をそれぞれ独立に扱う。
    function setupReorderIn(container, selector) {
        if (!container) return;

        const isTarget = (el) => el && el.matches && el.matches(selector);

        applyCardOrder(container);

        const items = Array.from(container.children).filter(isTarget);
        items.forEach(item => {
            // 既にハンドルがある場合は二重追加しない。
            if (item.querySelector(':scope > .drag-handle')) return;
            // Add a drag handle so the whole card can stay clickable.
            const handle = document.createElement('button');
            handle.type = 'button';
            handle.className = 'drag-handle';
            handle.textContent = '⣿';
            handle.setAttribute('aria-label', t('card.reorder'));
            handle.addEventListener('click', (e) => e.stopPropagation());
            item.appendChild(handle);
        });

        let dragging = null;
        let pointerId = null;

        function onDown(e, item) {
            // Only start a drag from the handle.
            if (!e.target.classList.contains('drag-handle')) return;
            e.preventDefault();
            dragging = item;
            pointerId = e.pointerId;
            item.classList.add('dragging');
            document.body.classList.add('card-dragging');
            try { item.setPointerCapture(e.pointerId); } catch (_) {}
        }

        function onMove(e) {
            if (!dragging) return;
            e.preventDefault();
            const target = document.elementFromPoint(e.clientX, e.clientY);
            const over = target && target.closest(selector);
            items.forEach(c => c.classList.remove('drop-target'));
            if (!over || over === dragging || !container.contains(over) || !isTarget(over)) return;
            over.classList.add('drop-target');
            const rect = over.getBoundingClientRect();
            const after = (e.clientY - rect.top) > rect.height / 2;
            container.insertBefore(dragging, after ? over.nextSibling : over);
        }

        function onUp() {
            if (!dragging) return;
            dragging.classList.remove('dragging');
            items.forEach(c => c.classList.remove('drop-target'));
            document.body.classList.remove('card-dragging');
            try { dragging.releasePointerCapture(pointerId); } catch (_) {}
            dragging = null;
            pointerId = null;
            saveCardOrder(container);
        }

        items.forEach(item => {
            item.addEventListener('pointerdown', (e) => onDown(e, item));
        });
        document.addEventListener('pointermove', onMove);
        document.addEventListener('pointerup', onUp);
        document.addEventListener('pointercancel', onUp);
    }

    // カードは .gauge-grid 内、セクションは .main 直下で、それぞれ独立に
    // 並べ替えられるようにする（カードとセクションは跨がない）。
    function setupCardReorder() {
        setupReorderIn(document.querySelector('.gauge-grid'), REORDER_CARD_SELECTOR);
        setupReorderIn(document.querySelector('.main'), REORDER_SECTION_SELECTOR);
    }

    // ============================================================
    // ★ 公開ビューアモード
    // 未ログイン（/api/auth/me が 401）の場合、サーバーが公開している
    // CPU/メモリ/ディスク使用率のカードだけを表示し、履歴・プラグイン・
    // アラートの各セクションや、権限が必要なナビは隠す。
    // データ自体はサーバー側ミドルウェアでも保護されている。
    // ============================================================
    // 権限に応じて表示を切り替える。サーバー側ミドルウェアのロールと揃える。
    //   role: 'admin' | 'operator' | 'viewer' | null（未ログインの公開ビューア）
    //   - 公開ビューア(null): CPU/メモリ/ディスク/稼働時間のみ。
    //   - viewer: 履歴・アラートは閲覧可（サーバーが viewer 以上に開放）。
    //     モジュール/プラグイン(operator)とアラートのクリア(operator)は隠す。
    //   - operator/admin: 全て表示。
    function applyAccessMode(role) {
        const rank = { viewer: 1, operator: 2, admin: 3 }[role] || 0;

        if (rank === 0) {
            // 公開ビューア（未ログイン）
            [elements.historySection, elements.backupSection, elements.alertsSection]
                .forEach(el => { if (el) el.style.display = 'none'; });

            // プロセス詳細（CPU/メモリ）とストレージ詳細（S.M.A.R.T の型番・
            // シリアル等）は公開しないため無効化する。
            [elements.cpuCard, elements.memCard, elements.diskCard].forEach(card => {
                if (!card) return;
                card.removeAttribute('data-click');
                card.removeAttribute('role');
                card.removeAttribute('tabindex');
                card.style.cursor = 'default';
            });

            document.querySelectorAll('.nav-tab[data-role-min]').forEach(el => {
                el.style.display = 'none';
            });
            const usersTab = document.querySelector('.nav-tab[href="/users.html"]');
            if (usersTab) usersTab.style.display = 'none';
            return;
        }

        if (rank < 2) {
            // viewer（ゲスト含む）: operator 以上の機能を隠す。
            if (elements.backupSection) elements.backupSection.style.display = 'none';
            if (elements.alertsClearBtn) elements.alertsClearBtn.style.display = 'none';
        }
    }

    function checkAccessMode() {
        // /api/auth/status は未認証でも 200 を返す。auth_enabled を見ないと、
        // 認証無効時に誤って表示を絞ってしまう。
        fetch('/api/auth/status', { credentials: 'same-origin' })
            .then(res => res.ok ? res.json() : null)
            .then(status => {
                if (!status) return;                        // 判定失敗時は通常モードのまま
                if (status.auth_enabled === false) return;  // 認証無効: 全開放
                applyAccessMode(status.authenticated ? status.role : null);
            })
            .catch(() => { /* 判定失敗時は通常モードのまま */ });
    }

    function init() {
        console.log(`[Kizuna-Eye] ${VERSION}`);
        drawInitialGauges();
        startClock();
        setupProcessCardClicks();
        setupStorageCardClick();
        setupCardReorder();
        checkAccessMode();
        connectWebSocket();
        loadAlerts();
        setInterval(loadAlerts, 30000);
        loadPluginList();
        setInterval(loadPluginList, 30000);
        loadHistory();
        setInterval(loadHistory, 30000);
        loadAlertThresholds();
        setInterval(loadAlertThresholds, 30000);

        // Re-render dynamic text on language change.
        window.addEventListener('kizuna-lang-change', () => {
            setConnectionState(currentConnectionState);
            // setTheme re-applies the theme label in the new language and, when
            // a status is present, re-renders the dashboard text.
            setTheme(getTheme());
            loadAlerts();
            loadPluginList();
            loadHistory();
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

})();