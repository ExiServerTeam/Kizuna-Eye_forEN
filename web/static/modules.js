// ============================================
// Kizuna-Eye - モジュール管理 JavaScript
// Phase 8-5: 手動実行ボタン追加
// ============================================

(function() {
    'use strict';

    const $ = (sel) => document.querySelector(sel);

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

    // Phase 8-6: プラグインメタのキャッシュ（name → PluginMeta）
    let pluginMetas = new Map();
    // 現在編集中のプラグイン名（動的フォーム用）
    let editingPluginName = null;

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
            if (data.event !== 'backup_result') return;
            const handler = backupResultHandlers.get(data.request_id);
            if (!handler) return;
            clearTimeout(handler.timer);
            backupResultHandlers.delete(data.request_id);
            handler.resolve(data);
        });
    }
    connectWS();

    function waitForBackupResult(requestID, timeoutMs) {
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                backupResultHandlers.delete(requestID);
                reject(new Error('タイムアウト（結果が返ってきませんでした）'));
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
        localStorage.setItem('kizuna-theme', theme);

        if (themeIcon) themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? 'ダーク' : 'ライト';
    }

    const savedTheme = localStorage.getItem('kizuna-theme') || 'dark';
    setTheme(savedTheme);

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
            showFieldError('modNameError', 'モジュール名は必須です');
            valid = false;
        } else if (!/^[a-zA-Z0-9_]+$/.test(nameVal)) {
            showFieldError('modNameError', '英数字とアンダースコアのみ使用できます');
            valid = false;
        } else {
            hideFieldError('modNameError');
        }

        const interval = parseInt(modInterval.value);
        if (isNaN(interval) || interval < 10 || interval > 3600) {
            showFieldError('modIntervalError', '10〜3600の範囲で指定してください');
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
            showToast('モジュール一覧の取得に失敗しました', 'error');
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
        const original = btn.textContent;
        btn.disabled = true;
        btn.textContent = '実行中...';

        try {
            const res = await fetch(`/api/plugins/${encodeURIComponent(name)}/run`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
            });
            const data = await res.json();
            if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);

            showToast('実行を受け付けました', 'info');
            const result = await waitForBackupResult(data.request_id, 180000);

            if (result.status === 'success') {
                showToast(`成功 (${formatBytes(result.size)}, ${result.duration_ms}ms)`, 'success');
            } else {
                showToast(`失敗: ${result.error || 'unknown'}`, 'error');
            }
            await loadModules();
        } catch (err) {
            showToast(`実行失敗: ${err.message}`, 'error');
        } finally {
            btn.disabled = false;
            btn.textContent = original;
        }
    }

    // ============================================================
    // 描画
    // ============================================================
    function renderModules() {
        if (!modules || modules.length === 0) {
            moduleGrid.innerHTML = `
                <div class="empty-state">
                    <h3>モジュールがありません</h3>
                    <p>「プラグイン追加」ボタンからモジュールを追加してください</p>
                    <button type="button" class="btn-primary empty-cta" id="emptyAddBtn">
                        <span class="btn-icon">＋</span> プラグイン追加
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

            const status = mod.status || {};
            const statusDotClass = status.status === 'success' ? 'success' :
                                  status.status === 'failed' ? 'failed' :
                                  status.status === 'running' ? 'running' : 'unknown';
            const statusLabel = status.status === 'success' ? '成功' :
                               status.status === 'failed' ? '失敗' :
                               status.status === 'running' ? '実行中' : '不明';

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

            const lastRun = status.last_run ? new Date(status.last_run).toLocaleString() : '--';
            const size = status.size || '--';
            const mode = status.mode || mod.config?.mode || '--';
            const targetDir = mod.config?.target_dir || '--';
            const logPath = mod.config?.log_path || '自動';
            const interval = mod.config?.interval_sec || mod.config?.interval || 60;

            // ★ Phase 8-5: プラグインのみ「▶ 実行」ボタンを表示
            const runButton = (mod.type === 'plugin')
                ? `<button class="btn-run" data-name="${escapeAttr(mod.name)}" title="手動実行">▶ 実行</button>`
                : '';

            html += `
                <div class="module-card">
                    <div class="module-card-header">
                        <div class="module-card-row">
                            <span class="badge ${versionBadge}">${versionLabel}</span>
                            <span class="module-name" title="${escapeAttr(displayLabel)}">${escapeHtml(displayLabel)}</span>
                            <span class="badge ${mod.enabled ? 'badge-enabled' : 'badge-disabled'}">
                                ${mod.enabled ? '有効' : '無効'}
                            </span>
                        </div>
                        <div class="module-card-actions">
                            ${runButton}
                            <button class="btn-edit" data-name="${escapeAttr(mod.name)}">編集</button>
                            <button class="btn-delete" data-name="${escapeAttr(mod.name)}">削除</button>
                        </div>
                    </div>
                    <div class="module-card-body">
                        <div class="module-info-row">
                            <span class="label">最終実行</span>
                            <span class="value">${lastRun}</span>
                        </div>
                        <div class="module-info-row">
                            <span class="label">状態</span>
                            <span class="value">
                                <span class="status-dot ${statusDotClass}"></span>${statusLabel}
                            </span>
                        </div>
                        <div class="module-info-row">
                            <span class="label">サイズ</span>
                            <span class="value">${size}</span>
                        </div>
                        <div class="module-info-row">
                            <span class="label">モード</span>
                            <span class="value">${escapeHtml(mode)}</span>
                        </div>
                        <div class="module-info-row">
                            <span class="label">対象ディレクトリ</span>
                            <span class="value" title="${escapeAttr(targetDir)}">${escapeHtml(targetDir)}</span>
                        </div>
                        <div class="module-info-row">
                            <span class="label">リモート接続先</span>
                            <span class="value" title="${escapeAttr(mod.config?.remote_dest || '')}">${escapeHtml(mod.config?.remote_dest || '--')}</span>
                        </div>
                    </div>
                    <div class="module-card-footer">
                        <span>チェック間隔: ${interval}秒</span>
                        <span>ログ: ${escapeHtml(logPath)}</span>
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
                        showToast('プラグインのメタ情報が見つかりません。再アップロードしてください', 'error');
                        return;
                    }
                    openPluginConfigModal(meta, mod.config || {});
                } else {
                    openModal('モジュール編集', mod);
                }
            });
        });

        // 削除ボタン
        document.querySelectorAll('.btn-delete').forEach(btn => {
            btn.addEventListener('click', () => {
                const name = btn.dataset.name;
                deleteTarget = name;
                deleteMessage.textContent = `本当にモジュール「${name}」を削除しますか？`;
                deleteModal.style.display = 'flex';
            });
        });
    }

    async function loadModules() {
        try {
            moduleGrid.innerHTML = '<div class="loading-spinner">読み込み中...</div>';

            const [mods, metas] = await Promise.all([
                fetchModules(),
                fetchPluginMetas(),
            ]);

            modules = mods;
            pluginMetas = metas;
            renderModules();
        } catch (err) {
            showToast('モジュール読み込みに失敗しました: ' + err.message, 'error');
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
            showToast('入力内容にエラーがあります', 'error');
            return;
        }

        const data = getFormData();

        try {
            if (editingName) {
                await updateModule(editingName, data);
                showToast('モジュールを更新しました', 'success');
            } else {
                await createModule(data);
                showToast('モジュールを追加しました', 'success');
            }
            closeModal();
            await loadModules();
        } catch (err) {
            showToast('保存に失敗しました: ' + err.message, 'error');
        }
    }

    async function handleDelete() {
        if (!deleteTarget) return;

        try {
            await deleteModule(deleteTarget);
            showToast(`モジュール「${deleteTarget}」を削除しました`, 'success');
            deleteModal.style.display = 'none';
            deleteTarget = null;
            await loadModules();
        } catch (err) {
            showToast('削除に失敗しました: ' + err.message, 'error');
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
                    showToast('無効なフォーマットです（配列が必要です）', 'error');
                    return;
                }
                modules = data;
                renderModules();
                showToast(`設定ファイルを読み込みました（${modules.length} モジュール）`, 'success');
            } catch (err) {
                showToast('JSONパースエラー: ' + err.message, 'error');
            }
        };
        reader.onerror = function() {
            showToast('ファイル読み込みエラー', 'error');
        };
        reader.readAsText(file);
    }

    function exportConfigFile() {
        if (modules.length === 0) {
            showToast('モジュールがありません', 'warning');
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
        showToast('設定ファイルを書き出しました', 'success');
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
            const pluginName = prompt('プラグイン名を入力してください（英数字・_・-）', defaultName);
            if (!pluginName) {
                e.target.value = '';
                return;
            }
            if (!/^[A-Za-z0-9_\-]+$/.test(pluginName)) {
                showToast('プラグイン名に使用できるのは英数字・_・- のみです', 'error');
                e.target.value = '';
                return;
            }

            const formData = new FormData();
            formData.append('name', pluginName);
            formData.append('plugin', file);

            try {
                showToast('プラグインをアップロード中...', 'info');

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
                    showToast('検証エラー: ' + result.inspect_error, 'error');
                    await loadModules();
                    e.target.value = '';
                    return;
                }

                showToast(`プラグイン「${result.name}」を登録しました`, 'success');

                openPluginConfigModal(result, {});
                await loadModules();
            } catch (err) {
                showToast('プラグイン読み込み失敗: ' + err.message, 'error');
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
            pluginConfigTitle.textContent = `プラグイン設定: ${meta.name}`;
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
            return `<p class="form-empty">このプラグインは設定項目を申告していません。</p>`;
        }

        const groups = new Map();
        for (const f of fields) {
            const g = f.group || '基本設定';
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
        const id = `cfg_${f.key}`;
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
                    <span>有効</span>
                </label>`;
                break;
            }
            case 'number': {
                const min = (f.min !== undefined) ? `min="${f.min}"` : '';
                const max = (f.max !== undefined) ? `max="${f.max}"` : '';
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
            showToast('プラグインのメタ情報が見つかりません', 'error');
            return;
        }

        const config = {};

        for (const f of (meta.fields || [])) {
            const el = document.getElementById(`cfg_${f.key}`);
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
                showToast(`入力エラー: ${f.label} は必須です`, 'error');
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
            showToast('プラグイン設定を保存しました', 'success');
            closePluginConfigModal();
            await loadModules();
        } catch (err) {
            showToast('保存に失敗しました: ' + err.message, 'error');
        }
    }

    // ============================================================
    // ユーティリティ
    // ============================================================
    function escapeHtml(s) {
        return String(s ?? '')
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }
    function escapeAttr(s) { return escapeHtml(s); }

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

})();
