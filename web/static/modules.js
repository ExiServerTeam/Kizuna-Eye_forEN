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

    // 現在のロール。auth 無効時は「全開放（信頼ネットワーク）」として扱う。
    let currentRole = null;   // 'admin' | 'operator' | 'viewer' | null
    let authEnabled = false;

    // Go 側 internal/api/modules.go の adminOnlyConfigFields と同じ集合。
    // ここに挙げたフィールドは admin のみ変更できる。UI では admin 以外には
    // 非表示にする（保存時は既存値をマージするため値は失われない）。
    // 変更時は modules.go と本リストの両方を揃えること（二重管理）。
    const ADMIN_ONLY_CONFIG_FIELDS = new Set([
        'block_duration_sec', 'block_mode', 'block_state_path', 'block_whitelist',
        'chain_key_path', 'cron_check', 'cron_paths', 'fim_watch',
        'fim_watch_baseline_path', 'fim_watch_paths', 'firewall_backend',
        'integrity_baseline_path', 'integrity_files', 'listen_port_check',
        'log_path', 'notify_min_level', 'plugin_path', 'suid_check',
        'suid_fast_check', 'suid_fast_paths', 'suid_paths', 'suid_watch',
        'suid_watch_paths', 'sudo_ignore_commands', 'watch_files',
    ]);

    // 認証無効時は全開放（ミドルウェアと同じ扱い）。
    function isAdmin() {
        return !authEnabled || currentRole === 'admin';
    }
    function canUploadPlugin() {
        // アップロードは常に認証必須（未認証RCE防止）なので admin のみ。
        return authEnabled && currentRole === 'admin';
    }
    function canDeleteModule() {
        return isAdmin();
    }
    function canEditModule() {
        return !authEnabled || currentRole === 'admin' || currentRole === 'operator';
    }
    // admin 以外には admin 限定フィールドを出さない。
    function visibleFields(fields) {
        if (isAdmin()) return fields || [];
        return (fields || []).filter(f => !ADMIN_ONLY_CONFIG_FIELDS.has(f.key));
    }

    // Latest backup status per plugin, updated from WS backup_result events.
    const moduleStatus = new Map();

    // Phase 8-6: プラグインメタのキャッシュ（name → PluginMeta）
    let pluginMetas = new Map();
    // Agent が報告したモジュールのロード結果（name → {status, reason, reason_code}）。
    // Agent だけが「実際にロードできたか」を観測できるため、これが無いと
    // ロードに失敗したモジュールも「監視中」に見えてしまう。
    let pluginRuntime = new Map();
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

            if (data.event === 'module_status') {
                // Agent が実際にロードできたかどうかを反映する。
                pluginRuntime = new Map();
                const statuses = data.statuses || {};
                Object.keys(statuses).forEach(k => pluginRuntime.set(k, statuses[k]));
                renderModules();
                return;
            }

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

        // アイコンは CSS が data-theme に応じて出し分ける（SVG）。触らない。
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
        if (!toastContainer) return;
        // 連打などで同じ内容のトーストが積み上がらないよう、既存の同一トーストは
        // 先に消してから出し直す。
        const key = type + '\u0000' + message;
        Array.from(toastContainer.children).forEach((c) => {
            if (c.dataset.toastKey === key) c.remove();
        });
        // 表示は最大 MAX_TOASTS 件まで。あふれた分は古い順に消す。
        const MAX_TOASTS = 4;
        while (toastContainer.children.length >= MAX_TOASTS) {
            toastContainer.removeChild(toastContainer.firstChild);
        }
        const toast = document.createElement('div');
        // 設定エディタと同じクラス構成（toast + 種別）にする。
        // 「toast-success」形式だと style.css の旧互換ルール
        // (.toast-success { background:#22c55e }) がマッチして緑ベタ塗りになる。
        toast.className = `toast ${type}`;
        toast.dataset.toastKey = key;
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
            // セレクタ文字列へ値を連結すると、値次第で別要素にマッチし得る。
            // 候補を走査して value を直接比較する。
            const radio = Array.prototype.find.call(
                document.querySelectorAll('input[name="modType"]'),
                r => r.value === type
            ) || null;
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

    // Agent が報告したモジュールのロード結果を取得する。
    async function fetchPluginRuntime() {
        try {
            const res = await fetch('/api/plugin-runtime');
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const data = await res.json();
            const map = new Map();
            const mods = data.modules || {};
            Object.keys(mods).forEach(k => map.set(k, mods[k]));
            return map;
        } catch (err) {
            console.error('Failed to fetch plugin runtime:', err);
            return new Map();
        }
    }

    // 現在のロールと認証有効/無効を取得する。
    async function fetchAuthStatus() {
        try {
            const res = await fetch('/api/auth/status', { credentials: 'same-origin' });
            if (!res.ok) return null;
            return await res.json();
        } catch (err) {
            return null;
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

    // 表示名（user_label）だけを書き換える。プラグインの内部名・.so・
    // modules.json は変更しないため、イベントやバックアップは壊れない。
    async function updatePluginLabel(name, userLabel) {
        const res = await fetch(`/api/plugins/${encodeURIComponent(name)}/label`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user_label: userLabel }),
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

        // Refuse to run when a required config field is empty. The backup
        // would otherwise fail on the agent side with a less obvious error.
        const runMeta = pluginMetas.get(name);
        const runMod = modules.find(m => m.name === name);
        if (runMeta && runMod) {
            const cfg = runMod.config || {};
            const missing = (runMeta.fields || []).filter(f => {
                if (!f.required) return false;
                const v = cfg[f.key];
                return v === '' || v === undefined || v === null;
            });
            if (missing.length > 0) {
                showToast(t('toast.required_fields_missing'), 'error');
                return;
            }
        }

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
                    ${canUploadPlugin() ? `<button type="button" class="btn-primary empty-cta" id="emptyAddBtn">
                        <span class="btn-icon">＋</span> ${t('modules.add_plugin_empty')}
                    </button>` : ''}
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

        // 権限で使えない操作（プラグイン追加＝.soアップロード）は UI ごと隠す。
        const pluginLabel = document.querySelector('label[for="pluginInput"]');
        if (pluginLabel) pluginLabel.style.display = canUploadPlugin() ? '' : 'none';

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

            // Badge rules:
            //   - pure (origin=official): honor the plugin's Tag() (GUARD/
            //     LITE/PRO) or fall back to a display_name hint, then PLUGIN.
            //   - third-party (origin=third_party): always PLUGIN. A
            //     community plugin must not be able to claim GUARD/LITE/PRO
            //     by naming its Tag() that way.
            // The card title prefers the operator's own label (user_label)
            // over the plugin's display_name, so the name they typed on
            // upload is what they see.
            let versionBadge, versionLabel, displayLabel;
            if (mod.type === 'plugin') {
                const meta = pluginMetas.get(mod.name);
                const displayName = meta?.display_name || '';
                const userLabel = (meta?.user_label || '').trim();
                displayLabel = userLabel || displayName || mod.name;
                const isThirdParty = meta?.origin === 'third_party';
                const declaredTag = isThirdParty
                    ? ''
                    : (meta?.tag || '').trim().toUpperCase();
                if (declaredTag) {
                    versionLabel = declaredTag;
                    versionBadge = declaredTag === 'GUARD' ? 'badge-guard'
                                 : declaredTag === 'LITE' ? 'badge-lite'
                                 : declaredTag === 'PRO' ? 'badge-pro'
                                 : 'badge-plugin';
                } else if (isThirdParty) {
                    versionBadge = 'badge-plugin';
                    versionLabel = 'PLUGIN';
                } else {
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
                }
            } else {
                versionBadge = (mod.config?.type === 'pro') ? 'badge-pro' : 'badge-lite';
                versionLabel = (mod.config?.type || 'pro').toUpperCase();
                displayLabel = mod.name;
            }

            const meta = pluginMetas.get(mod.name);
            // バックアップ系（is_backup=true）以外のプラグインは常時監視タイプとして扱う。
            const isSecurityPlugin = mod.type === 'plugin' && meta && meta.is_backup !== true;

            // Agent のロード結果。失敗していれば「監視中」ではなく「読み込み失敗」
            // として表示し、理由を UI 言語へ翻訳して出す。
            const runtime = pluginRuntime.get(mod.name);
            const runtimeFailed = runtime && runtime.status === 'failed';
            const failReason = (() => {
                if (!runtimeFailed) return '';
                if (runtime.reason_code) {
                    const key = 'modules.fail.' + runtime.reason_code;
                    const translated = t(key);
                    if (translated !== key) return translated;
                    return t('modules.fail.unknown');
                }
                // reason_code を持たない旧 Agent。生の理由は日本語の可能性が
                // あるため、UI が日本語のときだけ表示する。
                const lang = window.KizunaI18n ? window.KizunaI18n.getLang() : 'ja';
                if (lang === 'ja' && runtime.reason) return runtime.reason;
                return t('modules.fail.unknown');
            })();

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
            // 状態が一目で分かるよう、ボタン色を有効/無効で変える。
            //   有効 → 「無効化」（赤系・注意）
            //   無効 → 「有効化」（緑系・前向き）
            // 有効/無効の切り替えは監視の停止/開始に直結するため admin のみ。
            const toggleClass = mod.enabled ? 'btn-toggle on' : 'btn-toggle off';
            const toggleButton = isAdmin()
                ? `<button class="${toggleClass}" data-name="${escapeAttr(mod.name)}" data-enable="${mod.enabled ? '0' : '1'}">${mod.enabled ? t('modules.btn_disable') : t('modules.btn_enable')}</button>`
                : '';

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

            const failBody = `
                    <div class="module-info-row">
                        <span class="label">${t('security.label_state')}</span>
                        <span class="value"><span class="status-dot failed"></span>${t('modules.badge_load_failed')}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label">${t('modules.label_fail_reason')}</span>
                        <span class="value" title="${escapeAttr(failReason)}">${escapeHtml(failReason)}</span>
                    </div>
                    <div class="module-info-row">
                        <span class="label"></span>
                        <span class="value">${escapeHtml(t('modules.fail_hint'))}</span>
                    </div>
                `;

            // 権限のない操作はボタンを出さない（押して 403 になる UX を避ける）。
            const editButton = canEditModule()
                ? `<button class="btn-edit" data-name="${escapeAttr(mod.name)}">${t('modules.btn_edit')}</button>`
                : '';
            const deleteButton = canDeleteModule()
                ? `<button class="btn-delete" data-name="${escapeAttr(mod.name)}">${t('modules.btn_delete')}</button>`
                : '';

            const bodyHtml = runtimeFailed
                ? failBody
                : isSecurityPlugin
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
                            <span class="status-dot ${cssClass(statusDotClass)}"></span>${escapeHtml(statusLabel)}
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

            // 表示名を変更できるボタン。プラグインの内部名は変えず、
            // meta.json の user_label だけを書き換える（PUT /label）。
            const labelButton = (mod.type === 'plugin')
                ? `<button class="btn-label" data-name="${escapeAttr(mod.name)}" title="${escapeAttr(t('modules.btn_label_title'))}">${t('modules.btn_label')}</button>`
                : '';

            // 操作ボタン列の「つまみ」は出さない。
            // 並べ替えはカード本体のドラッグで行える（setupReorder が
            // つまみの無いカードでは余白ドラッグを許可する）。
            // つまみを戻したい場合は下の空文字を次のボタンに差し替える:
            //   `<button type="button" class="drag-handle" aria-label="${escapeAttr(t('card.reorder'))}" title="${escapeAttr(t('card.reorder'))}">⣿</button>`
            const dragHandle = '';

            html += `
                <div class="module-card" data-name="${escapeAttr(mod.name)}">
                    <div class="module-card-header">
                        <div class="module-card-row">
                            <span class="badge ${cssClass(versionBadge)}">${escapeHtml(versionLabel)}</span>
                            <span class="module-name" title="${escapeAttr(displayLabel)}">${escapeHtml(displayLabel)}</span>
                            ${runtimeFailed
                                ? `<span class="badge badge-failed">${t('modules.badge_load_failed')}</span>`
                                : `<span class="badge ${mod.enabled ? 'badge-enabled' : 'badge-disabled'}">
                                ${mod.enabled ? t('modules.badge_enabled') : t('modules.badge_disabled')}
                            </span>`}
                        </div>
                        <div class="module-card-actions">
                            ${openButton}
                            ${runButton}
                            ${toggleButton}
                            ${labelButton}
                            ${editButton}
                            ${deleteButton}
                            ${dragHandle}
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

        // 名前変更ボタン（表示名だけを書き換える）
        document.querySelectorAll('.btn-label').forEach(btn => {
            btn.addEventListener('click', () => openLabelModal(btn.dataset.name));
        });

        // ドラッグ & ドロップで並べ替え（順序は localStorage に保存）
        setupModuleReorder();
    }

    // ============================================================
    // モジュールカード並べ替え（ダッシュボードと同じ方式）
    // 順序は localStorage の kizuna-module-order に保存する。
    // ============================================================
    const MODULE_ORDER_KEY = 'kizuna-module-order';

    function saveModuleOrder() {
        try {
            const order = Array.from(moduleGrid.children)
                .filter(c => c.classList.contains('module-card'))
                .map(c => c.dataset.name);
            localStorage.setItem(MODULE_ORDER_KEY, JSON.stringify(order));
        } catch (e) { /* localStorage 不可の環境では無視 */ }
    }

    function applyModuleOrder() {
        let order;
        try {
            order = JSON.parse(localStorage.getItem(MODULE_ORDER_KEY) || '[]');
        } catch (e) { order = []; }
        if (!Array.isArray(order) || order.length === 0) return;

        const byName = new Map();
        Array.from(moduleGrid.children)
            .filter(c => c.classList.contains('module-card'))
            .forEach(card => byName.set(card.dataset.name, card));
        order.forEach(name => {
            const card = byName.get(name);
            if (card) moduleGrid.appendChild(card);
        });
    }

    function setupModuleReorder() {
        if (!moduleGrid) return;
        const isTarget = (el) => el && el.classList && el.classList.contains('module-card');
        applyModuleOrder();

        const items = Array.from(moduleGrid.children).filter(isTarget);
        let dragging = null;
        let pointerId = null;

        function onDown(e, item) {
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
            // ドラッグ中のカードは elementFromPoint の最前面にあり、その
            // 下にあるカードを拾えない。一時的に当たり判定から外すことで
            // 左右どちらへ動かしても正しい移動先を検出できる。
            dragging.style.pointerEvents = 'none';
            const target = document.elementFromPoint(e.clientX, e.clientY);
            const over = target && target.closest('.module-card');
            items.forEach(c => c.classList.remove('drop-target'));
            if (!over || over === dragging || !moduleGrid.contains(over) || !isTarget(over)) return;
            over.classList.add('drop-target');
            const rect = over.getBoundingClientRect();
            // グリッドは2次元配置なので、カーソルが over の中心から見て
            // X/Y のどちらに大きく外れているかで前後を決める。横長カード
            // では通常 X が支配的だが、行をまたぐときは Y が支配的になる。
            const cx = rect.left + rect.width / 2;
            const cy = rect.top + rect.height / 2;
            const dx = e.clientX - cx;
            const dy = e.clientY - cy;
            const after = Math.abs(dx) > Math.abs(dy) ? dx > 0 : dy > 0;
            moduleGrid.insertBefore(dragging, after ? over.nextSibling : over);
        }

        function onUp() {
            if (!dragging) return;
            dragging.classList.remove('dragging');
            dragging.style.pointerEvents = '';
            items.forEach(c => c.classList.remove('drop-target'));
            document.body.classList.remove('card-dragging');
            try { dragging.releasePointerCapture(pointerId); } catch (_) {}
            dragging = null;
            pointerId = null;
            saveModuleOrder();
        }

        items.forEach(item => {
            // 同じカードに二重登録しないよう、関数プロパティで印を付ける。
            if (item.dataset.reorderBound === '1') return;
            item.dataset.reorderBound = '1';
            item.addEventListener('pointerdown', (e) => onDown(e, item));
        });
        if (!moduleGrid.dataset.reorderGlobalBound) {
            moduleGrid.dataset.reorderGlobalBound = '1';
            document.addEventListener('pointermove', onMove);
            document.addEventListener('pointerup', onUp);
            document.addEventListener('pointercancel', onUp);
        }
    }

    // ============================================================
    // 表示名の変更
    // ============================================================
    function openLabelModal(name) {
        const mod = modules.find(m => m.name === name);
        const meta = pluginMetas.get(name);
        if (!mod || !meta) {
            showToast(t('toast.plugin_meta_missing'), 'error');
            return;
        }
        const current = (meta.user_label || meta.display_name || name);
        const input = prompt(t('modules.label_prompt'), current);
        if (input === null) return; // キャンセル
        const trimmed = input.trim();
        if ([...trimmed].length > 64) {
            showToast(t('toast.plugin_name_too_long'), 'error');
            return;
        }
        updatePluginLabel(name, trimmed)
            .then(() => {
                showToast(t('toast.plugin_label_saved'), 'success');
                return loadModules();
            })
            .catch((err) => showToast(t('toast.save_fail', err.message), 'error'));
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

            const [mods, metas, runtime, auth] = await Promise.all([
                fetchModules(),
                fetchPluginMetas(),
                fetchPluginRuntime(),
                fetchAuthStatus(),
            ]);

            if (auth) {
                authEnabled = auth.auth_enabled === true;
                currentRole = auth.authenticated ? auth.role : null;
            }

            modules = mods;
            pluginMetas = metas;
            pluginRuntime = runtime;
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
            // Deleting a plugin is destructive (the .so, meta and modules.json
            // entry are removed), so always show a red toast, regardless of
            // origin. The card title prefers user_label so the operator sees
            // the name they recognize.
            const mod = modules.find(m => m.name === deleteTarget);
            const meta = mod ? pluginMetas.get(mod.name) : null;
            const label = (meta?.user_label || meta?.display_name || deleteTarget);
            showToast(t('toast.module_deleted', label), 'error');
            deleteModal.style.display = 'none';
            deleteTarget = null;
            await loadModules();
        } catch (err) {
            showToast(t('toast.delete_fail', err.message), 'error');
        }
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

    // JSON読み込み: ローカルの modules.json を読み、画面表示を置き換える。
    // 注意: サーバーへは保存しない（保存は「保存」操作で行う）。表示確認用。
    function loadConfigFile(file) {
        const reader = new FileReader();
        reader.onload = function (e) {
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
        reader.onerror = function () {
            showToast(t('toast.file_read_error'), 'error');
        };
        reader.readAsText(file);
    }

    // ============================================================
    // Phase 8-6: プラグインアップロード → 動的フォーム表示
    // ============================================================
    if (pluginInput) {
        pluginInput.addEventListener('change', async (e) => {
            if (e.target.files.length === 0) return;

            // A-3: the .so and its detached signature can be selected
            // together (the input accepts multiple files), so one dialog is
            // enough. Any other file type is ignored.
            const files = Array.from(e.target.files);
            const soFiles = files.filter((f) => f.name.endsWith('.so'));
            const sigFiles = files.filter((f) => f.name.endsWith('.sig'));
            if (soFiles.length === 0) {
                showToast(t('toast.plugin_select_so'), 'error');
                e.target.value = '';
                return;
            }
            if (soFiles.length > 1) {
                showToast(t('toast.plugin_one_so'), 'error');
                e.target.value = '';
                return;
            }
            const file = soFiles[0];
            const signature = sigFiles.length > 0 ? sigFiles[0] : null;

            const defaultName = file.name.replace(/\.so$/, '');
            const pluginName = prompt(t('toast.plugin_name_prompt'), defaultName);
            if (!pluginName) {
                // The operator dismissed the name prompt before any upload
                // started. This is a routine cancel, so use a neutral green
                // toast ("Plugin installation was cancelled.").
                showToast(t('toast.plugin_cancelled'), 'success');
                e.target.value = '';
                return;
            }
            // The name is a display-only label, so any text is accepted
            // (including Japanese and spaces). The server derives the
            // filesystem name from the uploaded filename. Only an
            // excessively long label is rejected.
            if ([...pluginName].length > 64) {
                showToast(t('toast.plugin_name_too_long'), 'error');
                e.target.value = '';
                return;
            }

            const formData = new FormData();
            formData.append('name', pluginName);
            formData.append('plugin', file);
            if (signature) {
                // A-3: the detached signature is verified before the .so is
                // inspected and installed.
                formData.append('signature', signature);
            }

            try {
                showToast(t('toast.plugin_uploading'), 'info');

                const result = await uploadPlugin(formData);

                // The operator declined the unsigned-plugin confirmation.
                // The plugin looked like a third-party module, so this is a
                // warning-level cancel: use a red toast that names the reason.
                if (result && result.cancelled) {
                    showToast(t('toast.plugin_cancelled_third_party'), 'error');
                    e.target.value = '';
                    return;
                }

                if (result.inspect_error) {
                    showToast(t('toast.plugin_inspect_error', result.inspect_error), 'error');
                    await loadModules();
                    e.target.value = '';
                    return;
                }

                // Origin tells pure Kizuna plugins apart from third-party
                // ones. A third-party install is a deliberate risk, so it is
                // flagged in red even when the operator confirmed it.
                if (result.origin === 'official') {
                    showToast(t('toast.plugin_registered_official', result.name), 'success');
                } else {
                    showToast(t('toast.plugin_registered_third_party', result.name), 'error');
                }

                openPluginConfigModal(result, {});
                await loadModules();
            } catch (err) {
                showToast(t('toast.plugin_load_fail', err.message), 'error');
            }

            e.target.value = '';
        });
    }

    // uploadPlugin sends the .so (+ optional .sig). On 409 needs_confirmation
    // it shows the "not verified" overlay and retries with confirm_unsigned=true
    // only if the operator accepts the risk. Resolves with the server's JSON
    // meta on success, with { cancelled: true } when the operator declines,
    // and rejects with an Error on a real failure.
    async function uploadPlugin(formData) {
        const res = await fetch('/api/plugins/upload', { method: 'POST', body: formData });

        // 409 + needs_confirmation => unsigned plugin. Ask the operator.
        if (res.status === 409) {
            const body = await res.json().catch(() => ({}));
            if (body.needs_confirmation) {
                const ok = await confirmUnsignedPlugin(body.name || '');
                if (!ok) {
                    // Declined: not an error. Return a sentinel so the caller
                    // can show a neutral message instead of the error wrapper.
                    return { cancelled: true };
                }
                formData.append('confirm_unsigned', 'true');
                const retry = await fetch('/api/plugins/upload', { method: 'POST', body: formData });
                if (!retry.ok) {
                    const err = await retry.json().catch(() => ({}));
                    throw new Error(err.error || `HTTP ${retry.status}`);
                }
                return retry.json();
            }
            throw new Error(body.error || `HTTP ${res.status}`);
        }

        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `HTTP ${res.status}`);
        }
        return res.json();
    }

    // confirmUnsignedPlugin shows the overlay for an unsigned (third-party)
    // plugin and resolves true only when the operator explicitly accepts.
    function confirmUnsignedPlugin(name) {
        return new Promise((resolve) => {
            const overlay = $('#unsignedPluginModal');
            const nameEl = $('#unsignedPluginName');
            const okBtn = $('#unsignedPluginOk');
            const cancelBtn = $('#unsignedPluginCancel');
            if (!overlay || !okBtn || !cancelBtn) {
                resolve(window.confirm(t('plugins.unsigned_confirm_plain')));
                return;
            }
            if (nameEl) nameEl.textContent = name;
            overlay.style.display = 'flex';

            const cleanup = () => {
                overlay.style.display = 'none';
                okBtn.removeEventListener('click', onOk);
                cancelBtn.removeEventListener('click', onCancel);
            };
            const onOk = () => { cleanup(); resolve(true); };
            const onCancel = () => { cleanup(); resolve(false); };
            okBtn.addEventListener('click', onOk);
            cancelBtn.addEventListener('click', onCancel);
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
            // admin 以外には admin 限定フィールドを表示しない。
            pluginConfigBody.innerHTML = renderConfigForm(visibleFields(meta.fields), currentConfig || {});
        }
        if (pluginConfigModal) {
            pluginConfigModal.style.display = 'flex';
        }
    }

    function closePluginConfigModal() {
        editingPluginName = null;
        if (pluginConfigModal) pluginConfigModal.style.display = 'none';
    }

    // labelForOption returns the i18n label for a plugin-provided select
    // option (e.g. notify_min_level's "critical"/"warning"/"info"). Only
    // options with an "opt.<value>" entry are translated; anything else
    // falls back to the raw value the plugin declared, so unknown options
    // still display sanely.
    function labelForOption(value) {
        const key = 'opt.' + String(value).toLowerCase();
        const translated = t(key);
        return translated !== key ? translated : value;
    }

    // pickLang returns the English variant of a ConfigField string when the
    // UI is in English and the plugin supplied a translation, otherwise the
    // Japanese value. This keeps a plugin usable even if it only translated
    // some fields.
    function pickLang(jaValue, enValue) {
        const lang = (window.KizunaI18n && window.KizunaI18n.getLang)
            ? window.KizunaI18n.getLang()
            : 'ja';
        if (lang === 'en' && enValue) return enValue;
        return jaValue;
    }

    function renderConfigForm(fields, values) {
        if (!fields || fields.length === 0) {
            return `<p class="form-empty">${escapeHtml(t('modules.plugin_config_empty'))}</p>`;
        }

        const groups = new Map();
        for (const f of fields) {
            const g = pickLang(f.group, f.group_en) || t('modules.group_basic');
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
        const hintText = pickLang(f.hint, f.hint_en);
        const hint = hintText ? `<div class="field-hint">${escapeHtml(hintText)}</div>` : '';
        const placeholder = pickLang(f.placeholder, f.placeholder_en) || '';
        const labelText = pickLang(f.label, f.label_en);
        let input = '';

        switch (f.type) {
            case 'select':
                input = `<select id="${id}" name="${escapeAttr(f.key)}" ${required}>
                    ${(f.options || []).map(o => {
                        const sel = (o === value) ? 'selected' : '';
                        return `<option value="${escapeAttr(o)}" ${sel}>${escapeHtml(labelForOption(o))}</option>`;
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
                    placeholder="${escapeAttr(placeholder)}">`;
                break;
            }
            case 'password':
                input = `<input type="password" id="${id}" name="${escapeAttr(f.key)}"
                    value="${escapeAttr(value)}" ${required}
                    placeholder="${escapeAttr(placeholder)}"
                    autocomplete="new-password">`;
                break;
            case 'textarea':
                input = `<textarea id="${id}" name="${escapeAttr(f.key)}" ${required}
                    placeholder="${escapeAttr(placeholder)}">${escapeHtml(value)}</textarea>`;
                break;
            default:
                input = `<input type="text" id="${id}" name="${escapeAttr(f.key)}"
                    value="${escapeAttr(value)}" ${required}
                    placeholder="${escapeAttr(placeholder)}">`;
        }

        return `
            <div class="config-field" data-key="${escapeAttr(f.key)}">
                <label for="${id}">${escapeHtml(labelText)}${f.required ? ' <span class="req">*</span>' : ''}</label>
                ${input}
                ${hint}
            </div>
        `;
    }

    // collectFormValues reads the current values of the plugin config form into
    // a plain object. It is used by save and by the language-change redraw so
    // switching the UI language does not lose what the user typed.
    function collectFormValues(fields) {
        const config = {};
        for (const f of (fields || [])) {
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
        return config;
    }

    async function handlePluginConfigSave() {
        const name = editingPluginName;
        if (!name) return;

        const meta = pluginMetas.get(name);
        if (!meta) {
            showToast(t('toast.plugin_config_meta_missing'), 'error');
            return;
        }

        const config = collectFormValues(meta.fields);

        // 表示していない（admin 限定）フィールドは必須検証の対象外にする。
        // 表示フィールドは collectFormValues と同じ集合で判定する。
        for (const f of visibleFields(meta.fields)) {
            if (!f.required) continue;
            const v = config[f.key];
            if (v === '' || v === undefined || v === null) {
                showToast(t('toast.required_field', pickLang(f.label, f.label_en)), 'error');
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

    if (fileInput) {
        fileInput.addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                loadConfigFile(e.target.files[0]);
                e.target.value = '';
            }
        });
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

    // 言語変更時に再描画。プラグイン設定モーダルが開いていれば、
    // 入力中の値を読み取ってから描き直し、言語切替で入力が消えないようにする。
    window.addEventListener('kizuna-lang-change', () => {
        setTheme(getTheme());
        if (modules && modules.length > 0) renderModules();
        if (pluginConfigModal && pluginConfigModal.style.display === 'flex' && editingPluginName) {
            const meta = pluginMetas.get(editingPluginName);
            if (meta && pluginConfigBody) {
                const current = collectFormValues(meta.fields || []);
                pluginConfigBody.innerHTML = renderConfigForm(meta.fields || [], current);
            }
        }
    });

})();
