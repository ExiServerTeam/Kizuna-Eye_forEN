// ============================================================
// Kizuna-Eye Dashboard
// Gauge Engine v0.6.0
// ============================================================

(function () {
    'use strict';

    const VERSION = 'v0.6.0';

    const GAUGE = {
        radiusRatio: 0.40,
        lineWidthRatio: 0.20,
        glowBlur: 15
    };

    const ALERT = {
        mem:      { warn: 80, critical: 90, label: 'メモリ使用率' },
        diskFree: { warn: 20, critical: 10, label: 'ストレージ空き' },
        cpuTemp:  { warn: 70, critical: 85, label: 'CPU温度' },
        cpu:      { warn: 80, critical: 95, label: 'CPU使用率' },
        holdMs:   5000,
        cooldownMs: 5 * 60 * 1000
    };

    const $ = (selector) => document.querySelector(selector);

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
        backupLastRun: $('#backupLastRun'),
        backupStatus: $('#backupStatus'),
        backupSize: $('#backupSize'),
        backupNextRun: $('#backupNextRun'),
        backupToggle: $('#backupToggle'),
        backupBody: $('#backupBody'),
        backupToggleBtn: document.querySelector('.backup-toggle-btn'),
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
        processHint: $('#processHint')
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
    function setConnectionState(state) {
        if (!elements.connectionStatus) return;
        const el = elements.connectionStatus;
        el.classList.remove('connected', 'disconnected', 'connecting');
        el.classList.add(state);
        switch (state) {
            case 'connected':    el.textContent = '接続中';   break;
            case 'connecting':   el.textContent = '接続中...'; break;
            case 'disconnected': el.textContent = '切断';     break;
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
        localStorage.setItem('kizuna-theme', theme);

        if (elements.themeIcon) {
            elements.themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        }
        if (elements.themeLabel) {
            elements.themeLabel.textContent = theme === 'dark' ? 'ダーク' : 'ライト';
        }

        if (window._lastData) updateDashboard(window._lastData);
    }

    function toggleTheme() {
        const current = getTheme();
        setTheme(current === 'dark' ? 'light' : 'dark');
    }

    const savedTheme = localStorage.getItem('kizuna-theme') || 'dark';
    setTheme(savedTheme);

    if (elements.themeToggle) {
        elements.themeToggle.addEventListener('click', toggleTheme);
    }

    // ============================================================
    // ユーティリティ
    // ============================================================
    function clamp(value, min, max) {
        return Math.min(Math.max(Number(value) || 0, min), max);
    }

    function escapeHtml(value) {
        return String(value)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    }

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

        if (days > 0) return { value: `${days}日${hours}時間`, unit: '' };
        if (hours > 0) return { value: `${hours}時間${minutes}分`, unit: '' };
        if (minutes > 0) return { value: `${minutes}分${secs}秒`, unit: '' };
        return { value: `${secs}秒`, unit: '' };
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
    function showToast(message, type = 'info', icon = null) {
        if (!elements.toastContainer) return;
        const iconMap = {
            error: '🚨', warning: '⚠️', info: 'ℹ️', success: '✅'
        };
        const el = document.createElement('div');
        el.className = `dashboard-toast ${type}`;
        el.innerHTML = `
            <span class="toast-icon">${icon || iconMap[type] || 'ℹ️'}</span>
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

    function checkAlerts(cpuPercent, cpuTemp, memPercent, diskFreePercent) {
        let memLevel = null;
        if (memPercent >= ALERT.mem.critical) memLevel = 'critical';
        else if (memPercent >= ALERT.mem.warn) memLevel = 'warn';
        applyCardAlert(elements.memCard, memLevel);
        if (evaluateAlert('mem', memPercent, memPercent >= ALERT.mem.critical)) {
            showToast(`メモリ使用率が ${memPercent.toFixed(1)}% に達しました`, 'error', '🚨');
        }

        let diskLevel = null;
        if (diskFreePercent <= ALERT.diskFree.critical) diskLevel = 'critical';
        else if (diskFreePercent <= ALERT.diskFree.warn) diskLevel = 'warn';
        applyCardAlert(elements.diskCard, diskLevel);
        if (evaluateAlert('diskFree', diskFreePercent, diskFreePercent <= ALERT.diskFree.critical)) {
            showToast(`ストレージの空きが ${diskFreePercent.toFixed(1)}% を切りました`, 'error', '🚨');
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
            showToast(`CPU温度が ${Math.round(cpuTemp)}℃ に達しました`, 'error', '🌡️');
        }
        if (evaluateAlert('cpu', cpuPercent, cpuPercent >= ALERT.cpu.critical)) {
            showToast(`CPU使用率が ${cpuPercent.toFixed(1)}% に達しました`, 'warning', '⚠️');
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
    // 温度バッジ
    // ============================================================
    const NA_TOOLTIP = 'センサーの初期化中、または非対応の温度センサーです';

    function updateTempBadge(el, temp) {
        if (!el) return;
        const t = Number(temp);

        if (!Number.isFinite(t) || t <= 25) {
            el.hidden = false;
            el.textContent = 'N/A℃';
            el.classList.remove('cool', 'warm', 'hot', 'critical');
            el.classList.add('na');
            el.setAttribute('title', NA_TOOLTIP);
            return;
        }

        el.hidden = false;
        el.textContent = `${Math.round(t)}℃`;
        el.removeAttribute('title');
        el.classList.remove('na', 'cool', 'warm', 'hot', 'critical');

        if (t <= 45) el.classList.add('cool');
        else if (t <= 65) el.classList.add('warm');
        else if (t <= 85) el.classList.add('hot');
        else el.classList.add('critical');
    }

    // ============================================================
    // 空き容量バッジ
    // ============================================================
    function updateFreeBadge(el, freeBytes, totalBytes) {
        if (!el) return;
        const free = Math.max(0, Number(freeBytes) || 0);
        const total = Math.max(0, Number(totalBytes) || 0);
        if (total === 0) { el.hidden = true; return; }

        const freePercent = (free / total) * 100;
        el.hidden = false;
        el.textContent = `空き ${formatStorage(free)}`;
        el.classList.remove('warn', 'critical');
        el.setAttribute('title', `空き容量: ${freePercent.toFixed(1)}%`);

        if (freePercent < 10) el.classList.add('critical');
        else if (freePercent < 20) el.classList.add('warn');
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
            const freqText = f > 0 ? `${(f / 1000).toFixed(2)} GHz` : '周波数 N/A';
            html += `
                <div class="cpu-core-item">
                    <canvas id="core-gauge-${index}" width="64" height="64"></canvas>
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
            if (freePercent < 10) freeClass += ' critical';
            else if (freePercent < 20) freeClass += ' warn';

            let smartHtml = '';
            if (disk.health) {
                const healthClass = disk.health === 'PASSED' ? 'ok' :
                                   disk.health === 'FAILED' ? 'fail' : 'unknown';
                const healthLabel = disk.health === 'PASSED' ? '正常' :
                                   disk.health === 'FAILED' ? '異常' : '不明';
                smartHtml += `<span class="disk-health ${healthClass}" title="S.M.A.R.T: ${disk.health}">${healthLabel}</span>`;
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
                        <span class="${freeClass}" title="空き容量: ${freePercent.toFixed(1)}%">空き ${formatStorage(free)}</span>
                    </div>
                </div>
            `;
        });

        elements.diskList.innerHTML = html;
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
            const label = processState.sortKey === 'mem' ? 'メモリ順' : 'CPU順';
            elements.processModalTitle.textContent = `プロセス一覧 (Top 10 / ${label})`;
        }
        if (elements.processHint) {
            const label = processState.sortKey === 'mem' ? 'メモリ降順' : 'CPU降順';
            elements.processHint.textContent = `Top 10 / ${label} / 3秒ごと更新`;
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
                    ${processState.search ? '該当するプロセスがありません' : 'データ待機中...'}
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
                    <td class="col-cpu ${cpuClass}">${cpu.toFixed(1)}%</td>
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
                const label = processState.sortKey === 'mem' ? 'メモリ順' : 'CPU順';
                elements.processModalTitle.textContent = `プロセス一覧 (Top 10 / ${label})`;
            }
            if (elements.processHint) {
                const label = processState.sortKey === 'mem' ? 'メモリ降順' : 'CPU降順';
                elements.processHint.textContent = `Top 10 / ${label} / 3秒ごと更新`;
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
    function hasBackupData(backup) {
        if (!backup || typeof backup !== 'object') return false;
        const status = String(backup.status || '').toLowerCase();
        const hasStatus = status && status !== 'unknown';
        const hasLastRun = !!backup.last_run;
        const hasSize = Number(backup.size) > 0;
        return hasStatus || hasLastRun || hasSize;
    }

    function updateBackup(backup) {
        const section = elements.backupSection;
        if (!section) return;

        if (!hasBackupData(backup)) {
            section.hidden = true;
            return;
        }
        section.hidden = false;

        if (elements.backupLastRun && backup.last_run !== undefined) {
            elements.backupLastRun.textContent = backup.last_run || '未取得';
        }
        if (elements.backupStatus && backup.status !== undefined) {
            elements.backupStatus.textContent = backup.status || '-';
        }
        if (elements.backupSize && backup.size !== undefined) {
            elements.backupSize.textContent = formatBytes(backup.size);
        }
        if (elements.backupNextRun && backup.next_run !== undefined) {
            elements.backupNextRun.textContent = backup.next_run || '-';
        }

        if (elements.backupStatusBadge && backup.status !== undefined) {
            const badge = elements.backupStatusBadge;
            badge.className = 'backup-status-badge';
            switch (String(backup.status).toLowerCase()) {
                case 'success':
                case 'completed':
                    badge.textContent = '成功'; badge.classList.add('success'); break;
                case 'running':
                case 'processing':
                    badge.textContent = '実行中'; badge.classList.add('running'); break;
                case 'failed':
                case 'error':
                    badge.textContent = '失敗'; badge.classList.add('failed'); break;
                default:
                    badge.textContent = backup.status || '不明';
            }
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
        if (elements.cpuDetail) elements.cpuDetail.textContent = `使用率 ${cpu.toFixed(1)}%`;
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
        updateFreeBadge(elements.memFreeBadge, memoryTotal - memoryUsed, memoryTotal);
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
        updateFreeBadge(elements.diskFreeBadge, diskTotal - diskUsed, diskTotal);
        drawGauge(elements.diskGauge, diskPercent, 'disk');
        updateDiskList(disks);

        let maxDiskTemp = 0;
        if (Array.isArray(disks)) {
            disks.forEach(d => {
                const t = Number(d.temp) || 0;
                if (t > maxDiskTemp) maxDiskTemp = t;
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
            elements.uptimeDetail.textContent = `現在時刻: ${formatClock(new Date())}`;
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
            setConnectionState('connected');
        });

        socket.addEventListener('message', event => {
            try {
                const data = JSON.parse(event.data);
                window._lastData = data;
                updateDashboard(data);
            } catch (error) {
                console.error('[Kizuna-Eye] JSON:', error);
            }
        });

        socket.addEventListener('close', () => {
            setConnectionState('disconnected');
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
        if (elements.backupToggleBtn) {
            elements.backupToggleBtn.classList.toggle('collapsed', backupCollapsed);
        }
        if (elements.backupToggle) {
            elements.backupToggle.setAttribute('aria-expanded', String(!backupCollapsed));
        }
    }

    if (elements.backupToggle) {
        elements.backupToggle.addEventListener('click', toggleBackup);
    }

    // ============================================================
    // 初期描画
    // ============================================================
    function drawInitialGauges() {
        drawGauge(elements.cpuGauge, 0, 'cpu');
        drawGauge(elements.memGauge, 0, 'mem');
        drawGauge(elements.diskGauge, 0, 'disk');
    }

    function init() {
        console.log(`[Kizuna-Eye] ${VERSION}`);
        drawInitialGauges();
        startClock();
        setupProcessCardClicks();
        connectWebSocket();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

})();