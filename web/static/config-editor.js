// Phase 9-3 / GUI 設定エディタ
document.addEventListener('DOMContentLoaded', function () {
    'use strict';

    // ----- elements -----
    const configEditor = document.getElementById('configEditor');
    const editorTitle = document.getElementById('editorTitle');
    const formatBtn = document.getElementById('formatBtn');
    const validateBtn = document.getElementById('validateBtn');
    const saveBtn = document.getElementById('saveBtn');
    const validationStatus = document.getElementById('validationStatus');
    const charCount = document.getElementById('charCount');
    const lineCount = document.getElementById('lineCount');
    const fileBtns = document.querySelectorAll('.config-file-btn');
    const templateBtn = document.getElementById('templateBtn');

    const editorMode = document.getElementById('editorMode');
    const editorActions = document.getElementById('editorActions');
    const modeGuiBtn = document.getElementById('modeGuiBtn');
    const modeJsonBtn = document.getElementById('modeJsonBtn');
    const guiPanel = document.getElementById('guiPanel');
    const guiBody = document.getElementById('guiBody');
    const guiSaveBtn = document.getElementById('guiSaveBtn');

    // アラート閾値エディタ
    const alertConfigPanel = document.getElementById('alertConfigPanel');
    const alertConfigSaveBtn = document.getElementById('alertConfigSaveBtn');
    const acFields = {
        acMemWarn: document.getElementById('acMemWarn'),
        acMemCrit: document.getElementById('acMemCrit'),
        acDiskWarn: document.getElementById('acDiskWarn'),
        acDiskCrit: document.getElementById('acDiskCrit'),
        acTempWarn: document.getElementById('acTempWarn'),
        acTempCrit: document.getElementById('acTempCrit'),
        acNotifyRecovery: document.getElementById('acNotifyRecovery'),
    };

    let currentConfigType = 'agent';
    let currentMode = 'gui';
    let loadedConfig = {};

    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    // 設定タイプ → 画面に出すやさしい名前
    const CONFIG_TITLE_KEYS = {
        agent: 'config.file_agent',
        dashboard: 'config.file_dashboard',
    };
    function configTitle(type) {
        const k = CONFIG_TITLE_KEYS[type];
        return k ? t(k) : type;
    }

    // ============================================================
    // スキーマ定義
    // ============================================================
    const AGENT_GROUPS = [
        {
            legend: '接続先',
            fields: [
                { key: 'dashboard_url', label: 'ダッシュボードのURL', type: 'text', required: true, placeholder: 'ws://localhost:8080/ws', hint: '監視サーバーのアドレス。通常は「ws://ホスト名:8080/ws」の形式です。' },
            ],
        },
        {
            legend: '動作',
            fields: [
                { key: 'interval', label: 'データを送る間隔（秒）', type: 'number', min: 0.2, step: 0.1, hint: '小さくすると更新が細かくなります（0.2秒以上を推奨）。' },
            ],
        },
        {
            legend: 'ログと監視対象',
            fields: [
                { key: 'log_file', label: 'ログの保存先', type: 'text', placeholder: 'logs/agent.log', hint: '空の場合は logs/agent.log に保存されます。' },
                { key: 'disk_path', label: '監視するディスクのパス', type: 'text', placeholder: '/', required: true, hint: '空き容量を監視したいディスクのパス（例: / や D:\）。' },
            ],
        },
    ];

    const DASHBOARD_GROUPS = [
        {
            legend: 'サーバー設定',
            fields: [
                { key: 'listen_addr', label: '待ち受けアドレス', type: 'text', required: true, placeholder: ':8080', hint: 'ダッシュボードが接続を受け付けるアドレス（例: :8080）。' },
                { key: 'static_dir', label: '画面ファイルの場所', type: 'text', placeholder: './web/static', hint: '通常は変更不要です。' },
                { key: 'plugins_dir', label: 'プラグインの保存先', type: 'text', placeholder: '（空欄で自動）', hint: '空の場合は実行ファイルと同じ場所の plugins フォルダを使用します。' },
            ],
            checks: [
                { key: 'plugins_upload_enabled', label: '画面からプラグインをアップロードできるようにする' },
            ],
        },
        {
            legend: '認証・公開ビューア',
            prefix: 'auth.',
            checks: [
                { key: 'public_viewer', label: 'ゲストログインを許可する（ログインなしで CPU/メモリ/ディスク使用率を閲覧）' },
            ],
        },
        {
            legend: 'ログ',
            fields: [
                { key: 'log_file', label: 'ログの保存先', type: 'text', placeholder: 'dashboard.log', hint: '空の場合は標準出力に表示されます。' },
                { key: 'log_level', label: 'ログの詳しさ', type: 'select', options: ['debug', 'info', 'warn', 'error'], hint: 'debug が最も詳しく、error が最も少ない設定です。' },
            ],
        },
        {
            legend: 'アラート履歴',
            fields: [
                { key: 'alert_history_file', label: 'アラート履歴の保存先', type: 'text', placeholder: 'logs/alert_history.jsonl', hint: '空の場合は logs/alert_history.jsonl に保存されます。' },
            ],
        },
        {
            legend: '通知のON/OFF',
            prefix: 'notifications.',
            checks: [
                { key: 'enabled', label: 'アラートを通知する' },
            ],
        },
        {
            legend: '通知のタイミング',
            prefix: 'notifications.',
            fields: [
                { key: 'agent_timeout_sec', label: 'Agent の応答タイムアウト（秒）', type: 'number', min: 1, hint: 'この時間を超えて応答がないと「Agent 応答なし」と判定します。' },
                { key: 'hold_sec', label: '異常が続いたら通知するまでの時間（秒）', type: 'number', min: 0, hint: '一時的な数値の跳ね上がりで通知しないための待ち時間です。' },
                { key: 'cooldown_sec', label: '同じ通知を繰り返さない時間（秒）', type: 'number', min: 0, hint: '一度通知したあと、この時間は同じ通知を出しません。' },
                { key: 'recovery_hold_sec', label: '復旧通知を出すまでの時間（秒）', type: 'number', min: 0 },
            ],
        },
        {
            legend: 'アラートのしきい値',
            prefix: 'notifications.',
            fields: [
                { key: 'memory_warn_pct', label: 'メモリ使用率 警告（%）', type: 'number', min: 0, max: 100 },
                { key: 'memory_critical_pct', label: 'メモリ使用率 危険（%）', type: 'number', min: 0, max: 100 },
                { key: 'disk_free_warn_pct', label: 'ディスク空き容量 警告（%）', type: 'number', min: 0, max: 100, hint: '空き容量がこの割合を下回ると警告します。' },
                { key: 'disk_free_critical_pct', label: 'ディスク空き容量 危険（%）', type: 'number', min: 0, max: 100 },
                { key: 'cpu_temp_warn_c', label: 'CPU温度 警告（℃）', type: 'number', min: 0 },
                { key: 'cpu_temp_critical_c', label: 'CPU温度 危険（℃）', type: 'number', min: 0 },
            ],
            checks: [
                { key: 'notify_recovery', label: '復旧したときも通知する' },
            ],
        },
    ];

    // 通知チャンネルは Discord のみ対応
    const CHANNEL_TYPES = {
        discord: { label: 'Discord', fields: [{ key: 'webhook_url', label: 'Discord の Webhook URL', type: 'text', placeholder: 'https://discord.com/api/webhooks/...', hint: 'Discord のサーバー設定 → 連携サービス → ウェブフック で取得した URL を貼り付けてください。' }] },
    };

    // ============================================================
    // GUI フォーム生成
    // ============================================================
    function getByPath(obj, path) {
        return path.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj);
    }

    function buildField(f, value) {
        const wrap = document.createElement('div');
        wrap.className = 'config-field';
        const id = 'gf_' + f.key.replace(/\./g, '_');

        const label = document.createElement('label');
        label.setAttribute('for', id);
        label.textContent = f.label;
        if (f.required) {
            const req = document.createElement('span');
            req.className = 'req';
            req.textContent = ' *';
            label.appendChild(req);
        }
        wrap.appendChild(label);

        let input;
        if (f.type === 'select') {
            input = document.createElement('select');
            (f.options || []).forEach(opt => {
                const o = document.createElement('option');
                o.value = opt;
                o.textContent = opt;
                input.appendChild(o);
            });
        } else {
            input = document.createElement('input');
            input.type = f.type;
            if (f.type === 'number') {
                if (f.min !== undefined) input.min = f.min;
                if (f.max !== undefined) input.max = f.max;
                if (f.step !== undefined) input.step = f.step;
            }
            if (f.placeholder) input.placeholder = f.placeholder;
        }
        input.id = id;
        input.dataset.key = f.key;
        input.dataset.kind = f.type;
        if (value !== undefined && value !== null) input.value = value;
        wrap.appendChild(input);

        if (f.hint) {
            const hint = document.createElement('div');
            hint.className = 'field-hint';
            hint.textContent = f.hint;
            wrap.appendChild(hint);
        }
        return wrap;
    }

    function buildCheck(c, checked) {
        const wrap = document.createElement('div');
        wrap.className = 'config-field';
        const label = document.createElement('label');
        label.className = 'checkbox-wrap';
        const input = document.createElement('input');
        input.type = 'checkbox';
        input.id = 'gc_' + c.key.replace(/\./g, '_');
        input.dataset.key = c.key;
        input.dataset.kind = 'checkbox';
        input.checked = !!checked;
        const span = document.createElement('span');
        span.textContent = c.label;
        label.appendChild(input);
        label.appendChild(span);
        wrap.appendChild(label);
        return wrap;
    }

    function buildChannelCard(ch, origIndex) {
        const card = document.createElement('div');
        card.className = 'channel-card';
        // Remember the original position so collectGui can map this card back
        // to its source channel even after other cards are removed. A card
        // added by the user has no original index.
        if (typeof origIndex === 'number') {
            card.dataset.origIndex = String(origIndex);
        }

        const head = document.createElement('div');
        head.className = 'channel-head';

        const enabled = document.createElement('input');
        enabled.type = 'checkbox';
        enabled.checked = ch.enabled !== false;
        enabled.dataset.chField = 'enabled';
        head.appendChild(enabled);

        // GUI が編集できるのは Discord のみ。既存の他種別
        // (slack/telegram/line/email) は書き換えず、そのまま保持する。
        const channelType = ch.type || 'discord';
        const typeLabel = document.createElement('span');
        typeLabel.className = 'channel-type-label';
        typeLabel.textContent = channelType;
        head.appendChild(typeLabel);

        // 編集対象外の種別はフィールドを表示せず、値も保持する。
        // ただし head（有効/無効と種別ラベル）は表示する。
        if (channelType !== 'discord') {
            card.appendChild(head);
            const note = document.createElement('div');
            note.className = 'channel-note';
            note.textContent = t('config.channel_not_editable');
            card.appendChild(note);
            return card;
        }

        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'channel-remove';
        remove.textContent = t('config.channel_remove');
        remove.addEventListener('click', () => card.remove());
        head.appendChild(remove);

        card.appendChild(head);

        const fieldsWrap = document.createElement('div');
        card.appendChild(fieldsWrap);

        const def = CHANNEL_TYPES.discord;
        def.fields.forEach(f => {
            fieldsWrap.appendChild(buildField(f, ch[f.key]));
        });

        return card;
    }

    function buildChannelSection(channels) {
        const group = document.createElement('fieldset');
        group.className = 'config-group';
        const legend = document.createElement('legend');
        legend.textContent = t('config.channels');
        group.appendChild(legend);

        const list = document.createElement('div');
        list.id = 'channelList';
        // Guard against a malformed config where notifications.channels is not
        // an array (would otherwise throw and break the whole GUI).
        const arr = Array.isArray(channels) ? channels : [];
        arr.forEach((ch, i) => list.appendChild(buildChannelCard(ch, i)));
        group.appendChild(list);

        const addBtn = document.createElement('button');
        addBtn.type = 'button';
        addBtn.className = 'gui-add-channel';
        addBtn.textContent = '+ ' + t('config.add_channel');
        addBtn.addEventListener('click', () => {
            list.appendChild(buildChannelCard({ type: 'discord', enabled: true }));
        });
        group.appendChild(addBtn);
        return group;
    }

    function appendGuiHelp(key) {
        if (!key) return;
        const help = document.createElement('div');
        help.className = 'gui-help';
        help.textContent = t(key);
        guiBody.appendChild(help);
    }

    function renderGui() {
        guiBody.innerHTML = '';

        appendGuiHelp(currentConfigType === 'agent' ? 'config.gui_help_agent' : 'config.gui_help_dashboard');

        const groups = currentConfigType === 'agent' ? AGENT_GROUPS : DASHBOARD_GROUPS;
        groups.forEach(g => {
            const fs = document.createElement('fieldset');
            fs.className = 'config-group';
            const lg = document.createElement('legend');
            lg.textContent = g.legend;
            fs.appendChild(lg);

            (g.fields || []).forEach(f => {
                const path = (g.prefix || '') + f.key;
                const ff = Object.assign({}, f, { key: path });
                fs.appendChild(buildField(ff, getByPath(loadedConfig, path)));
            });
            (g.checks || []).forEach(c => {
                const path = (g.prefix || '') + c.key;
                const cc = Object.assign({}, c, { key: path });
                fs.appendChild(buildCheck(cc, getByPath(loadedConfig, path)));
            });
            guiBody.appendChild(fs);
        });

        if (currentConfigType === 'dashboard') {
            guiBody.appendChild(buildChannelSection(getByPath(loadedConfig, 'notifications.channels') || []));
        }
    }

    // Keys that must never be assigned while walking a path. Assigning to
    // __proto__ / constructor / prototype could pollute Object.prototype
    // (prototype pollution).
    const FORBIDDEN_KEYS = { __proto__: true, constructor: true, prototype: true };

    function setPath(obj, path, value) {
        const keys = path.split('.');
        let cur = obj;
        for (let i = 0; i < keys.length - 1; i++) {
            const k = keys[i];
            if (FORBIDDEN_KEYS[k]) return;
            if (typeof cur[k] !== 'object' || cur[k] === null) cur[k] = {};
            cur = cur[k];
        }
        const last = keys[keys.length - 1];
        if (FORBIDDEN_KEYS[last]) return;
        cur[last] = value;
    }

    function collectGui() {
        const out = JSON.parse(JSON.stringify(loadedConfig || {}));
        guiBody.querySelectorAll('input, select').forEach(el => {
            // 通知チャンネル内の入力欄は下の専用処理で収集する。
            // ここで拾うと webhook_url 等が設定直下に漏れてしまう。
            if (el.closest('#channelList')) return;
            const key = el.dataset.key;
            if (!key) return;
            let value;
            if (el.dataset.kind === 'checkbox') value = el.checked;
            else if (el.dataset.kind === 'number') value = el.value === '' ? 0 : Number(el.value);
            else value = el.value;
            setPath(out, key, value);
        });

        if (currentConfigType === 'dashboard') {
            const original = getByPath(loadedConfig, 'notifications.channels');
            const originalArr = Array.isArray(original) ? original : [];
            const channels = [];
            document.querySelectorAll('#channelList .channel-card').forEach((card) => {
                // 元の位置は data-orig-index で参照する（カード削除で
                // 位置がずれても別チャンネルのデータを混同しないため）。
                const origIdxRaw = card.dataset.origIndex;
                const orig = (origIdxRaw !== undefined) ? originalArr[Number(origIdxRaw)] : undefined;
                // 非 Discord チャンネルは編集対象外。元の値をそのまま残す。
                if (orig && orig.type && orig.type !== 'discord') {
                    channels.push(orig);
                    return;
                }
                const ch = {};
                card.querySelectorAll('[data-ch-field]').forEach(el => {
                    const f = el.dataset.chField;
                    if (f === 'enabled') ch.enabled = el.checked;
                });
                // 種別は Discord 固定（UI 上も選択式ではない）。
                // 保存時に type を落とすと通知チャンネルが復元されなくなる。
                ch.type = 'discord';
                card.querySelectorAll('[data-key]').forEach(el => {
                    const f = el.dataset.key;
                    if (el.dataset.kind === 'number') ch[f] = el.value === '' ? 0 : Number(el.value);
                    else ch[f] = el.value;
                });
                channels.push(ch);
            });
            setPath(out, 'notifications.channels', channels);
        }
        return out;
    }

    async function saveGui() {
        const payload = collectGui();
        try {
            const res = await fetch(`/api/config/${currentConfigType}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || `HTTP ${res.status}`);
            }
            loadedConfig = payload;
            showToast(t('config.saved'), 'success');
        } catch (err) {
            showToast(t('config.save_failed') + ': ' + err.message, 'error');
        }
    }

    if (guiSaveBtn) guiSaveBtn.addEventListener('click', saveGui);

    // ============================================================
    // モード切替
    // ============================================================
    function updateModeButtons() {
        modeGuiBtn.classList.toggle('active', currentMode === 'gui');
        modeJsonBtn.classList.toggle('active', currentMode === 'json');
    }

    function applyMode() {
        const isAlerts = currentConfigType === 'alerts';
        if (editorMode) editorMode.style.display = isAlerts ? 'none' : 'inline-flex';
        if (editorActions) editorActions.style.display = isAlerts ? 'none' : 'flex';

        if (isAlerts) {
            showAlertPanel();
            return;
        }

        if (currentMode === 'gui') {
            showGuiPanel();
        } else {
            showJsonEditor();
        }
    }

    modeGuiBtn.addEventListener('click', () => { currentMode = 'gui'; updateModeButtons(); applyMode(); });
    modeJsonBtn.addEventListener('click', () => { currentMode = 'json'; updateModeButtons(); applyMode(); });

    // ============================================================
    // パネル表示切替
    // ============================================================
    function setPanels(opts) {
        const editorBody = document.querySelector('.editor-body');
        const editorFooter = document.querySelector('.editor-footer');
        if (editorBody) editorBody.style.display = opts.json ? 'flex' : 'none';
        if (editorFooter) editorFooter.style.display = opts.json ? 'flex' : 'none';
        if (guiPanel) guiPanel.style.display = opts.gui ? 'flex' : 'none';
        if (alertConfigPanel) alertConfigPanel.style.display = opts.alerts ? 'flex' : 'none';
    }

    function showAlertPanel() {
        setPanels({ alerts: true });
        editorTitle.textContent = t('config.alerts_title');
        loadAlertConfig();
    }

    function showGuiPanel() {
        setPanels({ gui: true });
        editorTitle.textContent = configTitle(currentConfigType);
        renderGui();
    }

    function showJsonEditor() {
        setPanels({ json: true });
        editorTitle.textContent = configTitle(currentConfigType);
        updateCharCount();
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
        const themeIcon = document.getElementById('themeIcon');
        const themeLabel = document.getElementById('themeLabel');
        if (themeIcon) themeIcon.textContent = theme === 'dark' ? '🌙' : '☀️';
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
    }
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)');
    const savedTheme = localStorage.getItem('kizuna-theme');
    if (savedTheme) setTheme(savedTheme);
    else setTheme(prefersDark.matches ? 'dark' : 'light');
    prefersDark.addEventListener('change', (e) => {
        if (!localStorage.getItem('kizuna-theme')) setTheme(e.matches ? 'dark' : 'light');
    });
    const themeToggle = document.getElementById('themeToggle');
    if (themeToggle) {
        themeToggle.addEventListener('click', () => setTheme(getTheme() === 'dark' ? 'light' : 'dark'));
    }

    // ============================================================
    // 設定ファイル切替
    // ============================================================
    fileBtns.forEach(btn => {
        btn.addEventListener('click', () => {
            fileBtns.forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            currentConfigType = btn.dataset.type;
            if (currentConfigType === 'alerts') {
                applyMode();
            } else {
                loadConfig(currentConfigType);
            }
        });
    });

    // ============================================================
    // アラート設定
    // ============================================================
    async function loadAlertConfig() {
        try {
            const res = await fetch('/api/alert-config');
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const c = await res.json();
            if (acFields.acMemWarn) acFields.acMemWarn.value = c.memory_warn_pct;
            if (acFields.acMemCrit) acFields.acMemCrit.value = c.memory_critical_pct;
            if (acFields.acDiskWarn) acFields.acDiskWarn.value = c.disk_free_warn_pct;
            if (acFields.acDiskCrit) acFields.acDiskCrit.value = c.disk_free_critical_pct;
            if (acFields.acTempWarn) acFields.acTempWarn.value = c.cpu_temp_warn_c;
            if (acFields.acTempCrit) acFields.acTempCrit.value = c.cpu_temp_critical_c;
            if (acFields.acNotifyRecovery) acFields.acNotifyRecovery.checked = !!c.notify_recovery;
        } catch (err) {
            showToast(t('config.load_failed'), 'error');
        }
    }

    // numVal reads a numeric input by key, returning 0 when the element is
    // missing (defensive: a changed DOM must not throw here).
    function numVal(key) {
        const el = acFields[key];
        return el ? Number(el.value) : 0;
    }

    async function saveAlertConfig() {
        const payload = {
            memory_warn_pct: numVal('acMemWarn'),
            memory_critical_pct: numVal('acMemCrit'),
            disk_free_warn_pct: numVal('acDiskWarn'),
            disk_free_critical_pct: numVal('acDiskCrit'),
            cpu_temp_warn_c: numVal('acTempWarn'),
            cpu_temp_critical_c: numVal('acTempCrit'),
            notify_recovery: !!(acFields.acNotifyRecovery && acFields.acNotifyRecovery.checked),
        };
        try {
            const res = await fetch('/api/alert-config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || `HTTP ${res.status}`);
            }
            showToast(t('config.saved'), 'success');
        } catch (err) {
            showToast(t('config.save_failed'), 'error');
        }
    }
    if (alertConfigSaveBtn) alertConfigSaveBtn.addEventListener('click', saveAlertConfig);

    // ============================================================
    // JSON エディタ
    // ============================================================
    async function loadConfig(type) {
        try {
            const response = await fetch(`/api/config/${type}`);
            if (!response.ok) throw new Error(`Failed to load config: ${response.statusText}`);
            loadedConfig = await response.json();
            configEditor.value = JSON.stringify(loadedConfig, null, 2);
            updateValidationStatus(true);
            updateCharCount();
            applyMode();
        } catch (error) {
            console.error('Failed to load config:', error);
            showToast(t('config.load_failed'), 'error');
            updateValidationStatus(false, t('error.title'));
        }
    }

    function formatJSON() {
        try {
            const parsed = JSON.parse(configEditor.value);
            configEditor.value = JSON.stringify(parsed, null, 2);
            updateValidationStatus(true);
            showToast(t('config.formatted'), 'success');
        } catch (error) {
            updateValidationStatus(false, t('config.invalid_json'));
            showToast(t('config.invalid_json'), 'error');
        }
        updateCharCount();
    }

    function validateJSON() {
        try {
            JSON.parse(configEditor.value);
            updateValidationStatus(true);
            showToast(t('config.valid_json'), 'success');
        } catch (error) {
            updateValidationStatus(false, error.message);
            showToast(t('config.invalid_json'), 'error');
        }
    }

    async function saveConfig() {
        try {
            JSON.parse(configEditor.value);
        } catch (error) {
            updateValidationStatus(false, error.message);
            showToast(t('config.invalid_json'), 'error');
            return;
        }
        try {
            const response = await fetch(`/api/config/${currentConfigType}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: configEditor.value,
            });
            if (!response.ok) throw new Error(`Failed to save config: ${response.statusText}`);
            await response.json();
            showToast(t('config.saved'), 'success');
        } catch (error) {
            console.error('Failed to save config:', error);
            showToast(t('config.save_failed'), 'error');
        }
    }

    function updateValidationStatus(isValid, message = '') {
        validationStatus.classList.remove('valid', 'invalid');
        if (isValid) {
            validationStatus.classList.add('valid');
            validationStatus.querySelector('.status-text').textContent = t('config.valid_json');
        } else {
            validationStatus.classList.add('invalid');
            validationStatus.querySelector('.status-text').textContent = message || t('config.invalid_json');
        }
    }

    function updateCharCount() {
        const text = configEditor.value;
        charCount.textContent = t('config.chars', text.length);
        lineCount.textContent = t('config.lines', text.split('\n').length);
    }

    function showToast(message, type = 'info') {
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.textContent = message;
        document.getElementById('toastContainer').appendChild(toast);
        setTimeout(() => toast.remove(), 3000);
    }

    // ひな形挿入（JSON モード用）
    async function insertTemplate() {
        if (configEditor.value.trim() !== '' && !window.confirm(t('config.confirm_replace'))) return;
        let template = null;
        try {
            const res = await fetch(`/api/config/${currentConfigType}/template`);
            if (res.ok) template = await res.json();
        } catch (e) { /* fallback none */ }
        if (template === null) {
            showToast(t('config.no_template'), 'error');
            return;
        }
        configEditor.value = JSON.stringify(template, null, 2);
        updateCharCount();
        updateValidationStatus(true);
        showToast(t('config.template_inserted'), 'success');
    }

    if (templateBtn) templateBtn.addEventListener('click', insertTemplate);
    if (formatBtn) formatBtn.addEventListener('click', formatJSON);
    if (validateBtn) validateBtn.addEventListener('click', validateJSON);
    if (saveBtn) saveBtn.addEventListener('click', saveConfig);

    configEditor.addEventListener('input', () => {
        updateCharCount();
        try { JSON.parse(configEditor.value); updateValidationStatus(true); }
        catch (error) { updateValidationStatus(false); }
    });

    configEditor.addEventListener('keydown', (e) => {
        if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); saveConfig(); }
        if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key === 'F') { e.preventDefault(); formatJSON(); }
    });

    window.addEventListener('kizuna-lang-change', () => {
        updateCharCount();
        updateValidationStatus(true);
        if (currentConfigType !== 'alerts' && currentMode === 'gui') renderGui();
    });

    // 初期読み込み
    updateModeButtons();
    loadConfig(currentConfigType);
});
