// ============================================
// Kizuna-Eye - モジュール管理 JavaScript
// Phase 8-5: 手動実行ボタン追加
// ============================================

(function() {
    'use strict';

    const $ = (sel) => document.querySelector(sel);

    // i18n 翻訳ヘルパー（i18n.js 未読込時はキーをそのまま返す）。
    // 他ページの JS と同じフォールバックを持たせ、グローバル t への
    // 暗黙依存をなくす。
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    const moduleGrid = $('#moduleGrid');
    const totalCount = $('#totalCount');
    const enabledCount = $('#enabledCount');
    const disabledCount = $('#disabledCount');

    const modal = $('#moduleModal');
    const modalTitle = $('#modalTitle');
    const modalCloseBtn = $('#modalCloseBtn');
    const modalCancelBtn = $('#modalCancelBtn');
    const modalSaveBtn = $('#modalSaveBtn');
    const moduleForm = $('#moduleForm');

    const modName = $('#modName');
    const modTypeRadios = document.querySelectorAll('input[name="modType"]');
    const modConfigPath = $('#modConfigPath');
    const modLogPath = $('#modLogPath');
    const modInterval = $('#modInterval');
    const modTargetDir = $('#modTargetDir');
    const modRemoteDest = $('#modRemoteDest');
    const modEnabled = $('#modEnabled');

    const deleteModal = $('#deleteModal');
    const deleteMessage = $('#deleteMessage');
    const deleteConfirmBtn = $('#deleteConfirmBtn');
    const deleteCancelBtn = $('#deleteCancelBtn');
    const deleteCloseBtn = $('#deleteCloseBtn');

    // Phase 8-6: 動的プラグイン設定モーダル
    const pluginConfigModal = $('#pluginConfigModal');
    const pluginConfigTitle = $('#pluginConfigTitle');
    const pluginConfigBody = $('#pluginConfigBody');
    const pluginConfigCloseBtn = $('#pluginConfigCloseBtn');
    const pluginConfigCancelBtn = $('#pluginConfigCancelBtn');
    const pluginConfigSaveBtn = $('#pluginConfigSaveBtn');

    const toastContainer = $('#toastContainer');
    const themeToggle = $('#themeToggle');
    const themeIcon = $('#themeIcon');
    const themeLabel = $('#themeLabel');

    const fileInput = $('#fileInput');
    const pluginInput = $('#pluginInput');
    const exportFileBtn = $('#exportFileBtn');

    let modules = [];
    let editingName = null;
    let deleteTarget = null;

    // Latest backup status per plugin, updated from WS backup_result events.
    const moduleStatus = new Map();

    // Phase 8-6: プラグインメタのキャッシュ（name → PluginMeta）
    let pluginMetas = new Map();
    // 現在編集中のプラグイン名（動的フォーム用）
    let editingPluginName = null;

    // 実行中のプラグイン名。完了するまで再実行を受け付けない。
    const runningPlugins = new Set();
    // 手動実行中のプラグイン名。backup_result が届くまで解除しない
    // （定期ステータスの古い値で誤って解除されるのを防ぐ）。
    const manualRunning = new Set();

    const API_BASE = '/api/modules';

    // ============================================================
    // ★ Phase 8-5: WebSocket 接続（backup_result 受信用）
    // ============================================================
    let ws = null;
    const backupResultHandlers = new Map(); // request_id → {resolve, reject, timer}

    function connectWS() {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        ws = new WebSocket(`${proto}//${location.host}/ws`);

        ws.addEventListener('open', () => {
            console.log('[WS] connected');
        });
        ws.addEventListener('close', () => {
            console.log('[WS] disconnected, reconnecting in 3s...');
            setTimeout(connectWS, 3000);
        });
        ws.addEventListener('error', (e) => {
            console.error('[WS] error', e);
        });
        ws.addEventListener('message', (event) => {
            let data;
            try { data = JSON.parse(event.data); } catch { return; }

            if (data.event === 'backup_result') {
                console.log('[WS] backup_result', data);

                // Remember the latest status so the module card reflects it.
                if (data.plugin) {
                    runningPlugins.delete(data.plugin);
                    manualRunning.delete(data.plugin);
                    moduleStatus.set(data.plugin, {
                        status: data.status,
                        size: data.size,
                        last_run: data.timestamp,
                    });
                    renderModules();
                }

                const handler = backupResultHandlers.get(data.request_id);
                if (!handler) return;
                clearTimeout(handler.timer);
                backupResultHandlers.delete(data.request_id);
                handler.resolve(data);
                return;
            }

            // 定期ステータス（毎秒）から自動実行の「実行中」を反映する。
            // 手動実行と違い自動実行には request_id が無いため、
            // backup_result だけでは開始を検知できない。
            if (data && data.backup && data.backup.name) {
                const name = data.backup.name;
                const st = String(data.backup.status || '').toLowerCase();
                const isRunning = (st === 'running' || st === 'processing');

                let changed = false;
                if (isRunning && !runningPlugins.has(name)) {
                    runningPlugins.add(name);
                    changed = true;
                } else if (!isRunning && runningPlugins.has(name) && !manualRunning.has(name)) {
                    // 手動実行中は backup_result が届くまで解除しない。
                    runningPlugins.delete(name);
                    changed = true;
                }

                moduleStatus.set(name, {
                    status: data.backup.status,
                    size: data.backup.size,
                    last_run: data.backup.last_run,
                });

                if (changed) renderModules();
            }
        });
    }
    connectWS();

    function waitForBackupResult(requestID, timeoutMs) {
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                backupResultHandlers.delete(requestID);
                reject(new Error(t('toast.execution_timeout')));
            }, timeoutMs);
            backupResultHandlers.set(requestID, { resolve, reject, timer });
        });
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
        // 初期化 IIFE 全体を止めないよう握りつぶす。
        try { localStorage.setItem('kizuna-theme', theme); } catch (e) { /* ignore */ }

        if (themeIcon) themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
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

    if (themeToggle) {
        themeToggle.addEventListener('click', () => {
            const current = getTheme();
            setTheme(current === 'dark' ? 'light' : 'dark');
        });
    }

    // ============================================================
    // トースト
    // ============================================================
    function showToast(message, type = 'info') {
        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        toast.textContent = message;
        toastContainer.appendChild(toast);

        setTimeout(() => {
            toast.style.opacity = '0';
            toast.style.transform = 'translateX(100px)';
            toast.style.transition = 'opacity 0.3s, transform 0.3s';
            setTimeout(() => toast.remove(), 300);
        }, 4000);
    }

    // ============================================================
    // モーダル（既存・手動編集）
    // ============================================================
    function openModal(title, data) {
        modalTitle.textContent = title;
        modal.style.display = 'flex';

        if (data) {
            editingName = data.name;
            modName.value = data.name;
            modName.disabled = true;
            modConfigPath.value = data.config?.config_path || '';
            modLogPath.value = data.config?.log_path || '';
            modInterval.value = data.config?.interval || 60;
            modTargetDir.value = data.config?.target_dir || '';
            modRemoteDest.value = data.config?.remote_dest || '';
            modEnabled.checked = data.enabled !== false;

            const type = data.config?.type || 'pro';
            const radio = document.querySelector(`input[name="modType"][value="${type}"]`);
            if (radio) radio.checked = true;
            toggleConfigPath(type);
        } else {
            editingName = null;
            modName.value = '';
            modName.disabled = false;
            modConfigPath.value = '';
            modLogPath.value = '';
            modInterval.value = 60;
            modTargetDir.value = '';
            modRemoteDest.value = '';
            modEnabled.checked = true;
            const proRadio = document.querySelector('input[name="modType"][value="pro"]');
            if (proRadio) proRadio.checked = true;
            toggleConfigPath('pro');
        }

        document.querySelectorAll('.form-error').forEach(el => el.classList.remove('visible'));
        document.querySelectorAll('.form-group input').forEach(el => el.classList.remove('error'));
    }

    function closeModal() {
        modal.style.display = 'none';
        editingName = null;
    }

    function toggleConfigPath(type) {
        const group = $('#configPathGroup');
        if (group) {
            group.style.display = type === 'pro' ? 'block' : 'none';
        }
    }

    modTypeRadios.forEach(radio => {
        radio.addEventListener('change', () => {
            if (radio.checked) toggleConfigPath(radio.value);
        });
    });

    // ============================================================
    // バリデーション
    // ============================================================
    function validateForm() {
        let valid = true;

        const nameVal = modName.value.trim();
        if (!nameVal) {
            showFieldError('modNameError', t('validate.name_required'));
            valid = false;
        } else if (!/^[a-zA-Z0-9_]+$/.test(nameVal)) {
            showFieldError('modNameError', t('validate.name_format'));
            valid = false;
        } else {
            hideFieldError('modNameError');
        }

        const interval = parseInt(modInterval.value);
        if (isNaN(interval) || interval < 10 || interval > 3600) {
            showFieldError('modIntervalError', t('validate.interval_range'));
            valid = false;
        } else {
            hideFieldError('modIntervalError');
        }

        return valid;
    }

    function showFieldError(id, message) {
        const el = document.getElementById(id);
        if (el) {
            el.textContent = message;
            el.classList.add('visible');
        }
        const input = document.getElementById(id.replace('Error', ''));
        if (input) input.classList.add('error');
    }

    function hideFieldError(id) {
        const el = document.getElementById(id);
        if (el) el.classList.remove('visible');
        const input = document.getElementById(id.replace('Error', ''));
        if (input) input.classList.remove('error');
    }

    function getFormData() {
        const type = document.querySelector('input[name="modType"]:checked')?.value || 'pro';
        return {
            name: modName.value.trim(),
            type: 'backup',
            config: {
                type: type,
                config_path: modConfigPath.value.trim() || '',
                log_path: modLogPath.value.trim() || '',
                interval: parseInt(modInterval.value) || 60,
                target_dir: modTargetDir.value.trim() || '',
                remote_dest: modRemoteDest.value.trim() || '',
            },
            enabled: modEnabled.checked,
        };
    }

    // ============================================================
    // API
    // ============================================================
    async function fetchModules() {
        try {
            const res = await fetch(API_BASE);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            return await res.json();
        } catch (err) {
            console.error('Failed to fetch modules:', err);
            showToast(t('toast.modules_load_fail'), 'error');
            return [];
        }
    }

    // Phase 8-6: プラグインメタを取得
    async function fetchPluginMetas() {
        try {
            const res = await fetch('/api/plugins');
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const data = await res.json();
            const map = new Map();
            for (const meta of (data.plugins || [])) {
                map.set(meta.name, meta);
            }
            return map;
        } catch (err) {
            console.error('Failed to fetch plugin metas:', err);
            return new Map();
        }
    }

    async function createModule(data) {
        const res = await fetch(API_BASE, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${res.status}`);
        }
        return res.json();
    }

    async function updateModule(name, data) {
        const res = await fetch(`${API_BASE}/${encodeURIComponent(name)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${res.status}`);
        }
        return res.json();
    }

    async function deleteModule(name) {
        const res = await fetch(`${API_BASE}/${encodeURIComponent(name)}`, {
            method: 'DELETE',
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${res.status}`);
        }
        return res.json();
    }

    // ============================================================
    // ★ Phase 8-5: 手動実行
    // ============================================================
    async function handleRunClick(btn) {
        const name = btn.dataset.name;

        // すでに実行中なら受け付けない（二重実行の防止）
        if (runningPlugins.has(name)) {
            showToast(t('toast.execution_in_progress'), 'info');
            return;
        }
        runningPlugins.add(name);
        manualRunning.add(name);

        // Mark the card as running immediately.
        moduleStatus.set(name, { status: 'running', size: 0, last_run: '' });
        renderModules();

        try {
            const res = await fetch(`/api/plugins/${encodeURIComponent(name)}/run`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
            });
            const data = await res.json();
            if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);

            showToast(t('toast.execution_accepted'), 'info');
            // The server's run guard is released after 30 minutes
            // (runGuardTimeout). Wait slightly longer than that, so that if a
            // result never arrives the server-side guard has already expired
            // and the plugin can be run again (otherwise the UI shows
            // "not running" while the server answers 409).
            const result = await waitForBackupResult(data.request_id, 1860000);

            if (result.status === 'success') {
                showToast(t('toast.execution_success', formatBytes(result.size), result.duration_ms), 'success');
            } else {
                showToast(t('toast.execution_failed', result.error || 'unknown'), 'error');
            }
            await loadModules();
        } catch (err) {
            showToast(t('toast.execution_error', err.message), 'error');
            // Mark the card as failed so the state does not stay "running".
            moduleStatus.set(name, { status: 'error', size: 0, last_run: '' });
        } finally {
            runningPlugins.delete(name);
            manualRunning.delete(name);
            renderModules();
        }
    }

    // ============================================================
    // 描画
    // ============================================================
    function renderModules() {
        if (!modules || modules.length === 0) {
            moduleGrid.innerHTML = `
                <div class="empty-state">
                    <h3>${t('modules.empty_title')}</h3>
                    <p>${t('modules.empty_desc')}</p>
                    <button type="button" class="btn-primary empty-cta" id="emptyAddBtn">
                        <span class="btn-icon">＋</span> ${t('modules.add_plugin_empty')}
                    </button>
                </div>
            `;
            totalCount.textContent = '0';
            enabledCount.textContent = '0';
            disabledCount.textContent = '0';

            const emptyAddBtn = document.getElementById('emptyAddBtn');
            if (emptyAddBtn && pluginInput) {
                emptyAddBtn.addEventListener('click', () => {
                    pluginInput.click();
                });
            }
            return;
        }

        let html = '';
        let total = 0;
        let enabled = 0;
        let disabled = 0;

        modules.forEach(mod => {
            total++;
            if (mod.enabled) enabled++;
            else disabled++;

            const status = moduleStatus.get(mod.name) || mod.status || {};
            const s = String(status.status || '').toLowerCase();
            const statusDotClass = (s === 'success' || s === 'completed' || s === 'ok') ? 'success' :
                                  (s === 'failed' || s === 'error') ? 'failed' :
                                  (s === 'running' || s === 'processing') ? 'running' : 'unknown';
            const statusLabel = (s === 'success' || s === 'completed' || s === 'ok') ? t('status.success') :
                               (s === 'failed' || s === 'error') ? t('status.failed') :
                               (s === 'running' || s === 'processing') ? t('status.running') : t('status.unknown');

            // ★ Phase 8-6: バッジはLITE/PRO/PLUGIN、名前欄はdisplay_name
            let versionBadge, versionLabel, displayLabel;
            if (mod.type === 'plugin') {
                const meta = pluginMetas.get(mod.name);
                const displayName = meta?.display_name || '';
                displayLabel = displayName || mod.name;
                const upper = displayName.toUpperCase();
                if (upper.includes('LITE')) {
                    versionBadge = 'badge-lite';
                    versionLabel = 'LITE';
                } else if (upper.includes('PRO')) {
                    versionBadge = 'badge-pro';
                    versionLabel = 'PRO';
                } else {
                    versionBadge = 'badge-plugin';
                    versionLabel = 'PLUGIN';
                }
            } else {
                versionBadge = (mod.config?.type === 'pro') ? 'badge-pro' : 'badge-lite';
                versionLabel = (mod.config?.type || 'pro').toUpperCase();
                displayLabel = mod.name;
            }

            const meta = pluginMetas.get(mod.name);
            // バックアップ系（is_backup=true）以外のプラグインは常時監視タイプとして扱う。
            const isSecurityPlugin = mod.type === 'plugin' && meta && meta.is_backup !== true;

            const lastRun = status.last_run ? new Date(status.last_run).toLocaleString() : '--';
            const size = Number(status.size) > 0 ? formatBytes(status.size) : '--';
            const mode = status.mode || mod.config?.mode || '--';
            const targetDir = mod.config?.target_dir || '--';
            const logPath = mod.config?.log_path || t('common.auto');
            const interval = mod.config?.interval_sec || mod.config?.poll_interval_sec || mod.config?.interval || 60;

            // 「実行」ボタンは手動実行できるバックアップ系プラグインのみ表示する。
            // 常時監視するタイプ（セキュリティ等）は実行不要なので出さない。
            const canRun = mod.type === 'plugin' && meta && meta.is_backup === true;
            const isRunning = runningPlugins.has(mod.name);
            const runButton = canRun
                ? `<button class="btn-run" data-name="${escapeAttr(mod.name)}" title="Run"${isRunning ? ' disabled' : ''}>${isRunning ? t('status.running') + '...' : t('modules.btn_run')}</button>`
                : '';

            // プラグイン専用 Web UI があれば「開く」ボタンを表示
            const openButton = (mod.type === 'plugin' && meta && meta.has_web_ui)
                ? `<a class="btn-open" href="/plugins/${encodeURIComponent(mod.name)}/" target="_blank" rel="noopener">${t('modules.btn_open')}</a>`
                : '';

            // 有効/無効の切り替えボタン
            const toggleButton = `<button class="btn-toggle" data-name="${escapeAttr(mod.name)}" data-enable="${mod.enabled ? '0' : '1'}">${mod.enabled ? t('modules.btn_disable') : t('modules.btn_enable')}</button>`;

            // カード本文。セキュリティ等の監視プラグインは専用の項目を表示する。
            const cfg = mod.config || {};
            const watchFiles = cfg.watch_files || '--';
            const notifyLevel = cfg.notify_min_level
                ? t('level.' + String(cfg.notify_min_level).toLowerCase())
                : '--';
            const burstText = cfg.failed_burst
                ? t('security.burst_value', cfg.failed_burst, cfg.burst_window_sec || '-')
                : '--';
            const monitorState = mod.enabled ? t('security.state_active') : t('security.state_inactive');

            const bodyHtml = isSecurityPlugin
                ? `
                    <div class="module-info-row">
                        <span class="label">${t('security.label_state')}</span>
                        <span class="value"><span class="status-dot ${mod.enabled ? 'success' : 'unknown'}"></span>${escapeHtml(monitorState)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('security.label_watch')}</span>
                        <span class="value" title="${escapeAttr(watchFiles)}">${escapeHtml(watchFiles)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('security.label_notify')}</span>
                        <span class="value">${escapeHtml(notifyLevel)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('security.label_burst')}</span>
                        <span class="value">${escapeHtml(burstText)}</span>
                    </div>
                `
                : `
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_last_run')}</span>
                        <span class="value">${escapeHtml(lastRun)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_status')}</span>
                        <span class="value">
                            <span class="status-dot ${statusDotClass}"></span>${escapeHtml(statusLabel)}
                        </span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_size')}</span>
                        <span class="value">${escapeHtml(size)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_mode')}</span>
                        <span class="value">${escapeHtml(mode)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_target')}</span>
                        <span class="value" title="${escapeAttr(targetDir)}">${escapeHtml(targetDir)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_remote')}</span>
                        <span class="value" title="${escapeAttr(mod.config?.remote_dest || '')}">${escapeHtml(mod.config?.remote_dest || '--')}</span>
                    </div>
                `;

            html += `
                <div class="module-card">
                    <div class="module-card-header">
                        <div class="module-card-row">
                            <span class="badge ${versionBadge}">${escapeHtml(versionLabel)}</span>
                            <span class="module-name" title="${escapeAttr(displayLabel)}">${escapeHtml(displayLabel)}</span>
                            <span class="badge ${mod.enabled ? 'badge-enabled' : 'badge-disabled'}">
                                ${mod.enabled ? t('modules.badge_enabled') : t('modules.badge_disabled')}
                            </span>
                        </div>
                        <div class="module-card-actions">
                            ${openButton}
                            ${runButton}
                            ${toggleButton}
                            <button class="btn-edit" data-name="${escapeAttr(mod.name)}">${t('modules.btn_edit')}</button>
                            <button class="btn-delete" data-name="${escapeAttr(mod.name)}">${t('modules.btn_delete')}</button>
                        </div>
                    </div>
                    <div class="module-card-body">
                        ${bodyHtml}
                    </div>
                    <div class="module-card-footer">
                        <span>${escapeHtml(t('modules.label_interval', interval))}</span>
                        <span>${t('modules.label_log', escapeHtml(logPath))}</span>
                    </div>
                </div>
            `;
        });

        moduleGrid.innerHTML = html;
        totalCount.textContent = total;
        enabledCount.textContent = enabled;
        disabledCount.textContent = disabled;

        // ★ Phase 8-5: 実行ボタンのイベント登録
        document.querySelectorAll('.btn-run').forEach(btn => {
            btn.addEventListener('click', () => handleRunClick(btn));
        });

        // 編集ボタン
        document.querySelectorAll('.btn-edit').forEach(btn => {
            btn.addEventListener('click', () => {
                const name = btn.dataset.name;
                const mod = modules.find(m => m.name === name);
                if (!mod) return;

                if (mod.type === 'plugin') {
                    const meta = pluginMetas.get(name);
                    if (!meta) {
                        showToast(t('toast.plugin_meta_missing'), 'error');
                        return;
                    }
                    openPluginConfigModal(meta, mod.config || {});
                } else {
                    openModal(t('modules.modal_title_edit'), mod);
                }
            });
        });

        // 有効/無効 切り替えボタン
        document.querySelectorAll('.btn-toggle').forEach(btn => {
            btn.addEventListener('click', () => handleToggleClick(btn));
        });

        // 削除ボタン
        document.querySelectorAll('.btn-delete').forEach(btn => {
            btn.addEventListener('click', () => {
                const name = btn.dataset.name;
                deleteTarget = name;
                deleteMessage.textContent = t('modules.delete_message', name);
                deleteModal.style.display = 'flex';
            });
        });
    }

    // ============================================================
    // 有効/無効 切り替え
    // ============================================================
    async function handleToggleClick(btn) {
        const name = btn.dataset.name;
        const enable = btn.dataset.enable === '1';
        const mod = modules.find(m => m.name === name);
        if (!mod) return;

        btn.disabled = true;
        try {
            const payload = {
                name: mod.name,
                type: mod.type,
                enabled: enable,
                config: mod.config || {},
            };
            await updateModule(name, payload);
            showToast(enable ? t('toast.module_enabled') : t('toast.module_disabled'), 'success');
            await loadModules();
        } catch (err) {
            showToast(t('toast.save_fail', err.message), 'error');
            btn.disabled = false;
        }
    }

    async function loadModules() {
        try {
            moduleGrid.innerHTML = `<div class="loading-spinner">${t('modules.loading')}</div>`;

            const [mods, metas] = await Promise.all([
                fetchModules(),
                fetchPluginMetas(),
            ]);

            modules = mods;
            pluginMetas = metas;
            renderModules();
        } catch (err) {
            showToast(t('toast.modules_load_fail_msg', err.message), 'error');
            moduleGrid.innerHTML = `
                <div class="empty-state">
                    <h3>読み込みエラー</h3>
                    <p>${escapeHtml(err.message)}</p>
                </div>
            `;
        }
    }

    // ============================================================
    // 保存・削除（既存手動モーダル用）
    // ============================================================
    async function handleSave() {
        if (!validateForm()) {
            showToast(t('toast.invalid_input'), 'error');
            return;
        }

        const data = getFormData();

        try {
            if (editingName) {
                await updateModule(editingName, data);
                showToast(t('toast.module_saved'), 'success');
            } else {
                await createModule(data);
                showToast(t('toast.module_added'), 'success');
            }
            closeModal();
            await loadModules();
        } catch (err) {
            showToast(t('toast.save_fail', err.message), 'error');
        }
    }

    async function handleDelete() {
        if (!deleteTarget) return;

        try {
            await deleteModule(deleteTarget);
            showToast(t('toast.module_deleted', deleteTarget), 'success');
            deleteModal.style.display = 'none';
            deleteTarget = null;
            await loadModules();
        } catch (err) {
            showToast(t('toast.delete_fail', err.message), 'error');
        }
    }

    // ============================================================
    // JSON読み込み
    // ============================================================
    function loadConfigFile(file) {
        const reader = new FileReader();
        reader.onload = function(e) {
            try {
                const data = JSON.parse(e.target.result);
                if (!Array.isArray(data)) {
                    showToast(t('toast.json_invalid'), 'error');
                    return;
                }
                modules = data;
                renderModules();
                showToast(t('toast.json_loaded', modules.length), 'success');
            } catch (err) {
                showToast(t('toast.json_parse_error', err.message), 'error');
            }
        };
        reader.onerror = function() {
            showToast(t('toast.file_read_error'), 'error');
        };
        reader.readAsText(file);
    }

    function exportConfigFile() {
        if (modules.length === 0) {
            showToast(t('toast.no_modules'), 'warning');
            return;
        }

        const data = JSON.stringify(modules, null, 2);
        const blob = new Blob([data], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'modules.json';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
        showToast(t('toast.export_done'), 'success');
    }

    if (fileInput) {
        fileInput.addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                loadConfigFile(e.target.files[0]);
                e.target.value = '';
            }
        });
    }

    // ============================================================
    // Phase 8-6: プラグインアップロード → 動的フォーム表示
    // ============================================================
    if (pluginInput) {
        pluginInput.addEventListener('change', async (e) => {
            if (e.target.files.length === 0) return;

            const file = e.target.files[0];

            const defaultName = file.name.replace(/\.so$/, '');
            const pluginName = prompt(t('toast.plugin_name_prompt'), defaultName);
            if (!pluginName) {
                e.target.value = '';
                return;
            }
            if (!/^[A-Za-z0-9_\-]+$/.test(pluginName)) {
                showToast(t('toast.plugin_name_invalid'), 'error');
                e.target.value = '';
                return;
            }

            const formData = new FormData();
            formData.append('name', pluginName);
            formData.append('plugin', file);

            try {
                showToast(t('toast.plugin_uploading'), 'info');

                const res = await fetch('/api/plugins/upload', {
                    method: 'POST',
                    body: formData,
                });

                if (!res.ok) {
                    const err = await res.json().catch(() => ({}));
                    throw new Error(err.error || `HTTP ${res.status}`);
                }

                const result = await res.json();

                if (result.inspect_error) {
                    showToast(t('toast.plugin_inspect_error', result.inspect_error), 'error');
                    await loadModules();
                    e.target.value = '';
                    return;
                }

                showToast(t('toast.plugin_registered', result.name), 'success');

                openPluginConfigModal(result, {});
                await loadModules();
            } catch (err) {
                showToast(t('toast.plugin_load_fail', err.message), 'error');
            }

            e.target.value = '';
        });
    }

    // ============================================================
    // Phase 8-6: 動的フォーム生成
    // ============================================================
    function openPluginConfigModal(meta, currentConfig) {
        editingPluginName = meta.name;
        if (pluginConfigTitle) {
            pluginConfigTitle.textContent = t('modules.plugin_config_title', meta.name);
        }
        if (pluginConfigBody) {
            pluginConfigBody.innerHTML = renderConfigForm(meta.fields || [], currentConfig || {});
        }
        if (pluginConfigModal) {
            pluginConfigModal.style.display = 'flex';
        }
    }

    function closePluginConfigModal() {
        editingPluginName = null;
        if (pluginConfigModal) pluginConfigModal.style.display = 'none';
    }

    function renderConfigForm(fields, values) {
        if (!fields || fields.length === 0) {
            return `<p class="form-empty">${escapeHtml(t('modules.plugin_config_empty'))}</p>`;
        }

        const groups = new Map();
        for (const f of fields) {
            const g = f.group || t('modules.group_basic');
            if (!groups.has(g)) groups.set(g, []);
            groups.get(g).push(f);
        }

        const html = [];
        for (const [groupName, groupFields] of groups.entries()) {
            html.push(`<fieldset class="config-group"><legend>${escapeHtml(groupName)}</legend>`);
            for (const f of groupFields) {
                html.push(renderField(f, values[f.key]));
            }
            html.push(`</fieldset>`);
        }
        return html.join('');
    }

    function renderField(f, current) {
        const value = (current !== undefined && current !== null)
            ? String(current)
            : (f.default || '');
        // Build a safe element id from the key. Only [A-Za-z0-9_-] is kept, so
        // a plugin-provided key can neither break out of id="..." nor make the
        // id ambiguous with getElementById().
        const id = fieldElementId(f.key);
        const required = f.required ? 'required' : '';
        const hint = f.hint ? `<div class="field-hint">${escapeHtml(f.hint)}</div>` : '';
        let input = '';

        switch (f.type) {
            case 'select':
                input = `<select id="${id}" name="${escapeAttr(f.key)}" ${required}>
                    ${(f.options || []).map(o => {
                        const sel = (o === value) ? 'selected' : '';
                        return `<option value="${escapeAttr(o)}" ${sel}>${escapeHtml(o)}</option>`;
                    }).join('')}
                </select>`;
                break;
            case 'checkbox': {
                const checked = (value === 'true' || value === true) ? 'checked' : '';
                input = `<label class="checkbox-wrap">
                    <input type="checkbox" id="${id}" name="${escapeAttr(f.key)}" ${checked}>
                    <span>${escapeHtml(t('modules.enabled'))}</span>
                </label>`;
                break;
            }
            case 'number': {
                // min/max come from the plugin's metadata; escape them so a
                // non-numeric value cannot break out of the attribute.
                const min = (f.min !== undefined && f.min !== null) ? `min="${escapeAttr(f.min)}"` : '';
                const max = (f.max !== undefined && f.max !== null) ? `max="${escapeAttr(f.max)}"` : '';
                input = `<input type="number" id="${id}" name="${escapeAttr(f.key)}"
                    value="${escapeAttr(value)}" ${min} ${max} ${required}
                    placeholder="${escapeAttr(f.placeholder || '')}">`;
                break;
            }
            case 'password':
                input = `<input type="password" id="${id}" name="${escapeAttr(f.key)}"
                    value="${escapeAttr(value)}" ${required}
                    placeholder="${escapeAttr(f.placeholder || '')}"
                    autocomplete="new-password">`;
                break;
            case 'textarea':
                input = `<textarea id="${id}" name="${escapeAttr(f.key)}" ${required}
                    placeholder="${escapeAttr(f.placeholder || '')}">${escapeHtml(value)}</textarea>`;
                break;
            default:
                input = `<input type="text" id="${id}" name="${escapeAttr(f.key)}"
                    value="${escapeAttr(value)}" ${required}
                    placeholder="${escapeAttr(f.placeholder || '')}">`;
        }

        return `
            <div class="config-field" data-key="${escapeAttr(f.key)}">
                <label for="${id}">${escapeHtml(f.label)}${f.required ? ' <span class="req">*</span>' : ''}</label>
                ${input}
                ${hint}
            </div>
        `;
    }

    async function handlePluginConfigSave() {
        const name = editingPluginName;
        if (!name) return;

        const meta = pluginMetas.get(name);
        if (!meta) {
            showToast(t('toast.plugin_config_meta_missing'), 'error');
            return;
        }

        const config = {};

        for (const f of (meta.fields || [])) {
            // Never let a plugin-provided key pollute Object.prototype.
            if (isUnsafeKey(f.key)) continue;
            const el = document.getElementById(fieldElementId(f.key));
            if (!el) continue;
            if (f.type === 'checkbox') {
                config[f.key] = !!el.checked;
            } else if (f.type === 'number') {
                const n = Number(el.value);
                config[f.key] = Number.isFinite(n) ? n : 0;
            } else {
                config[f.key] = el.value;
            }
        }

        for (const f of (meta.fields || [])) {
            if (!f.required) continue;
            const v = config[f.key];
            if (v === '' || v === undefined || v === null) {
                showToast(t('toast.required_field', f.label), 'error');
                return;
            }
        }

        const existing = modules.find(m => m.name === name);

        try {
            if (existing) {
                const newConfig = Object.assign({}, existing.config, config);
                const payload = {
                    name: existing.name,
                    type: existing.type,
                    enabled: existing.enabled,
                    config: newConfig,
                };
                await updateModule(name, payload);
            } else {
                const payload = {
                    name: name,
                    type: 'plugin',
                    enabled: true,
                    config: config,
                };
                await createModule(payload);
            }
            showToast(t('toast.plugin_config_saved'), 'success');
            closePluginConfigModal();
            await loadModules();
        } catch (err) {
            showToast(t('toast.save_fail', err.message), 'error');
        }
    }

    // ============================================================
    // ユーティリティ
    // ============================================================
    // escapeHtml / escapeAttr は escape.js（共有ユーティリティ）で定義される。
    // プラグイン由来の文字列（meta.fields / options / hint など）は必ず通すこと。

    // fieldElementId builds a DOM id from a plugin-provided config key.
    // Only [A-Za-z0-9_-] is kept so the value is safe inside id="..." and
    // stable for getElementById().
    function fieldElementId(key) {
        return 'cfg_' + String(key).replace(/[^A-Za-z0-9_-]/g, '_');
    }

    // isUnsafeKey reports whether a plugin-provided key could pollute
    // Object.prototype if used as an object property name.
    function isUnsafeKey(key) {
        const k = String(key);
        return k === '__proto__' || k === 'constructor' || k === 'prototype';
    }

    function formatBytes(bytes) {
        bytes = Number(bytes) || 0;
        if (bytes === 0) return '0 B';
        const k = 1024;
        const units = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return (bytes / Math.pow(k, i)).toFixed(2) + ' ' + units[i];
    }

    if (exportFileBtn) {
        exportFileBtn.addEventListener('click', exportConfigFile);
    }

    // ============================================================
    // イベント登録
    // ============================================================
    if (modalCloseBtn) modalCloseBtn.addEventListener('click', closeModal);
    if (modalCancelBtn) modalCancelBtn.addEventListener('click', closeModal);
    if (modal) {
        modal.addEventListener('click', (e) => {
            if (e.target === modal) closeModal();
        });
    }
    if (modalSaveBtn) modalSaveBtn.addEventListener('click', handleSave);

    if (moduleForm) {
        moduleForm.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                handleSave();
            }
        });
    }

    if (pluginConfigCloseBtn) pluginConfigCloseBtn.addEventListener('click', closePluginConfigModal);
    if (pluginConfigCancelBtn) pluginConfigCancelBtn.addEventListener('click', closePluginConfigModal);
    if (pluginConfigModal) {
        pluginConfigModal.addEventListener('click', (e) => {
            if (e.target === pluginConfigModal) closePluginConfigModal();
        });
    }
    if (pluginConfigSaveBtn) pluginConfigSaveBtn.addEventListener('click', handlePluginConfigSave);

    if (deleteCloseBtn) deleteCloseBtn.addEventListener('click', () => {
        deleteModal.style.display = 'none';
        deleteTarget = null;
    });
    if (deleteCancelBtn) deleteCancelBtn.addEventListener('click', () => {
        deleteModal.style.display = 'none';
        deleteTarget = null;
    });
    if (deleteModal) {
        deleteModal.addEventListener('click', (e) => {
            if (e.target === deleteModal) {
                deleteModal.style.display = 'none';
                deleteTarget = null;
            }
        });
    }
    if (deleteConfirmBtn) deleteConfirmBtn.addEventListener('click', handleDelete);

    // ============================================================
    // 初期化
    // ============================================================
    loadModules();

    // 言語変更時に再描画
    window.addEventListener('kizuna-lang-change', () => {
        setTheme(getTheme());
        if (modules && modules.length > 0) renderModules();
    });

})();
