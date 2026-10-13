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
    const historyBtn = document.getElementById('historyBtn');
    const historyModal = document.getElementById('historyModal');
    const historyList = document.getElementById('historyList');
    const historyCloseBtn = document.getElementById('historyCloseBtn');

    const editorMode = document.getElementById('editorMode');
    const editorActions = document.getElementById('editorActions');
    const modeGuiBtn = document.getElementById('modeGuiBtn');
    const modeJsonBtn = document.getElementById('modeJsonBtn');
    const guiPanel = document.getElementById('guiPanel');
    const guiBody = document.getElementById('guiBody');
    // 保存ボタンはヘッダー上部の #saveBtn（GUI/JSON 用）と、アラートパネル
    // 内の #alertConfigSaveBtn（アラートはヘッダー操作群を隠すため）の2つ。

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
    // ラベル・ヒント・凡例は i18n キーで保持し、描画時に t() で翻訳する。
    // これにより英語 UI でも設定エディタが英語になる。
    const AGENT_GROUPS = [
        {
            legendKey: 'config.g.agent.conn',
            fields: [
                { key: 'dashboard_url', labelKey: 'config.f.dashboard_url', type: 'text', required: true, placeholder: 'ws://localhost:8080/ws', hintKey: 'config.h.dashboard_url' },
            ],
        },
        {
            legendKey: 'config.g.agent.behavior',
            fields: [
                { key: 'interval', labelKey: 'config.f.interval', type: 'number', min: 1, step: 1, hintKey: 'config.h.interval' },
            ],
        },
        {
            legendKey: 'config.g.agent.log',
            fields: [
                { key: 'log_file', labelKey: 'config.f.agent_log_file', type: 'text', placeholder: 'logs/agent.log', hintKey: 'config.h.agent_log_file' },
                { key: 'disk_path', labelKey: 'config.f.disk_path', type: 'text', placeholder: '/', required: true, hintKey: 'config.h.disk_path' },
            ],
        },
    ];

    const DASHBOARD_GROUPS = [
        {
            legendKey: 'config.g.dash.server',
            fields: [
                { key: 'listen_addr', labelKey: 'config.f.listen_addr', type: 'text', required: true, placeholder: ':8080', hintKey: 'config.h.listen_addr' },
                { key: 'static_dir', labelKey: 'config.f.static_dir', type: 'text', placeholder: './web/static', hintKey: 'config.h.static_dir' },
                { key: 'plugins_dir', labelKey: 'config.f.plugins_dir', type: 'text', placeholder: '', hintKey: 'config.h.plugins_dir' },
            ],
            checks: [
                { key: 'plugins_upload_enabled', labelKey: 'config.c.plugins_upload_enabled' },
            ],
        },
        {
            legendKey: 'config.g.dash.auth',
            prefix: 'auth.',
            checks: [
                // 未設定のときは有効（既定ON）。キーが無ければチェックを入れる。
                { key: 'public_viewer', labelKey: 'config.c.public_viewer', defaultChecked: true },
            ],
        },
        {
            legendKey: 'config.g.dash.log',
            fields: [
                { key: 'log_file', labelKey: 'config.f.dash_log_file', type: 'text', placeholder: 'dashboard.log', hintKey: 'config.h.dash_log_file' },
                { key: 'log_level', labelKey: 'config.f.log_level', type: 'select', options: ['debug', 'info', 'warn', 'error'], hintKey: 'config.h.log_level' },
            ],
        },
        {
            legendKey: 'config.g.dash.history',
            fields: [
                { key: 'alert_history_file', labelKey: 'config.f.alert_history_file', type: 'text', placeholder: 'logs/alert_history.jsonl', hintKey: 'config.h.alert_history_file' },
            ],
        },
        {
            legendKey: 'config.g.dash.notif_toggle',
            prefix: 'notifications.',
            checks: [
                { key: 'enabled', labelKey: 'config.c.notif_enabled' },
            ],
        },
        {
            legendKey: 'config.g.dash.notif_timing',
            prefix: 'notifications.',
            fields: [
                { key: 'agent_timeout_sec', labelKey: 'config.f.agent_timeout_sec', type: 'number', min: 1, hintKey: 'config.h.agent_timeout_sec' },
                { key: 'hold_sec', labelKey: 'config.f.hold_sec', type: 'number', min: 0, hintKey: 'config.h.hold_sec' },
                { key: 'cooldown_sec', labelKey: 'config.f.cooldown_sec', type: 'number', min: 0, hintKey: 'config.h.cooldown_sec' },
                { key: 'recovery_hold_sec', labelKey: 'config.f.recovery_hold_sec', type: 'number', min: 0 },
            ],
        },
        {
            legendKey: 'config.g.dash.thresholds',
            prefix: 'notifications.',
            fields: [
                { key: 'memory_warn_pct', labelKey: 'config.f.memory_warn_pct', type: 'number', min: 0, max: 100 },
                { key: 'memory_critical_pct', labelKey: 'config.f.memory_critical_pct', type: 'number', min: 0, max: 100 },
                { key: 'disk_free_warn_pct', labelKey: 'config.f.disk_free_warn_pct', type: 'number', min: 0, max: 100, hintKey: 'config.h.disk_free_warn_pct' },
                { key: 'disk_free_critical_pct', labelKey: 'config.f.disk_free_critical_pct', type: 'number', min: 0, max: 100 },
                { key: 'cpu_temp_warn_c', labelKey: 'config.f.cpu_temp_warn_c', type: 'number', min: 0 },
                { key: 'cpu_temp_critical_c', labelKey: 'config.f.cpu_temp_critical_c', type: 'number', min: 0 },
            ],
            checks: [
                { key: 'notify_recovery', labelKey: 'config.c.notify_recovery' },
            ],
        },
    ];

    // 通知チャンネルは Discord のみ対応
    const CHANNEL_TYPES = {
        discord: { label: 'Discord', fields: [{ key: 'webhook_url', labelKey: 'config.f.webhook_url', type: 'text', placeholder: 'https://discord.com/api/webhooks/...', hintKey: 'config.h.webhook_url' }] },
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
        label.textContent = f.labelKey ? t(f.labelKey) : (f.label || '');
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
            hint.textContent = f.hintKey ? t(f.hintKey) : (f.hint || '');
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
        span.textContent = c.labelKey ? t(c.labelKey) : (c.label || '');
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
            lg.textContent = g.legendKey ? t(g.legendKey) : (g.legend || '');
            fs.appendChild(lg);

            (g.fields || []).forEach(f => {
                const path = (g.prefix || '') + f.key;
                const ff = Object.assign({}, f, { key: path });
                fs.appendChild(buildField(ff, getByPath(loadedConfig, path)));
            });
            (g.checks || []).forEach(c => {
                const path = (g.prefix || '') + c.key;
                const cc = Object.assign({}, c, { key: path });
                // キーが未設定なら defaultChecked（未設定＝有効な項目）を使う。
                let val = getByPath(loadedConfig, path);
                if (val === undefined || val === null) val = cc.defaultChecked;
                fs.appendChild(buildCheck(cc, val));
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
        // collectGui() can throw (unexpected DOM state). Keep it inside try so
        // a failure still produces an error toast instead of a silent failure.
        try {
            const payload = collectGui();
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
            dirty = false;
            showToast(t('config.saved'), 'success');
        } catch (err) {
            showToast(t('config.save_failed') + ': ' + (err && err.message ? err.message : err), 'error');
        }
    }

    // GUI フォームの変更は「離脱時保存」の対象として記録するだけ。
    if (guiBody) {
        guiBody.addEventListener('input', markDirty);
        guiBody.addEventListener('change', markDirty);
    }

    // ============================================================
    // モード切替
    // ============================================================
    function updateModeButtons() {
        modeGuiBtn.classList.toggle('active', currentMode === 'gui');
        modeJsonBtn.classList.toggle('active', currentMode === 'json');
    }

    // モードに応じてツールバーのボタンを出し分ける。
    //   - 保存       : 全モード共通（常に表示）
    //   - ひな形挿入 : JSON モード専用（JSON テキストを置き換えるため）
    //   - フォーマット / 検証 : JSON モード専用（JSON テキストへの操作）
    // GUI モードでは JSON 専用ボタンを隠し、操作を分かりやすくする。
    function applyActionVisibility() {
        const isAlerts = currentConfigType === 'alerts';
        const showJsonActions = !isAlerts && currentMode === 'json';
        const set = (el, visible) => {
            if (el) el.style.display = visible ? '' : 'none';
        };
        set(templateBtn, showJsonActions);
        set(formatBtn, showJsonActions);
        set(validateBtn, showJsonActions);
        // 履歴はエージェント/ダッシュボード設定でのみ表示（アラートは対象外）。
        set(historyBtn, !isAlerts);
        // 保存ボタンは常に表示する（GUI/JSON どちらの保存手段。アラート時は
        // editorActions 全体が隠れるため、ここで個別に触る必要はない）。
    }

    function applyMode() {
        const isAlerts = currentConfigType === 'alerts';
        // アラートは専用パネルなので GUI/JSON 切替を隠す。操作ボタン群は
        // applyActionVisibility が出し分けを担当する。
        if (editorMode) editorMode.style.display = isAlerts ? 'none' : 'inline-flex';
        if (editorActions) editorActions.style.display = isAlerts ? 'none' : 'flex';

        applyActionVisibility();

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
        // アラートは専用パネルなので JSON 専用ボタンを隠す。
        applyActionVisibility();
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
        // localStorage はプライベートモード等で例外を投げる。失敗しても
        // DOMContentLoaded ハンドラ全体を止めないよう握りつぶす。
        try { localStorage.setItem('kizuna-theme', theme); } catch (e) { /* ignore */ }
        const themeIcon = document.getElementById('themeIcon');
        const themeLabel = document.getElementById('themeLabel');
        // アイコンは CSS が data-theme に応じて出し分ける（SVG）。触らない。
        if (themeLabel) themeLabel.textContent = theme === 'dark' ? t('theme.dark') : t('theme.light');
    }
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)');
    let savedTheme = null;
    try { savedTheme = localStorage.getItem('kizuna-theme'); } catch (e) { /* ignore */ }
    if (savedTheme) setTheme(savedTheme);
    else setTheme(prefersDark.matches ? 'dark' : 'light');
    prefersDark.addEventListener('change', (e) => {
        let manual = null;
        try { manual = localStorage.getItem('kizuna-theme'); } catch (err) { /* ignore */ }
        if (!manual) setTheme(e.matches ? 'dark' : 'light');
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
                // 専用パネル（アラート）は applyMode 経由で表示する。
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

    // アラート閾値の入力値を集める。保存と離脱時保存の両方で使う。
    function collectAlertPayload() {
        return {
            memory_warn_pct: numVal('acMemWarn'),
            memory_critical_pct: numVal('acMemCrit'),
            disk_free_warn_pct: numVal('acDiskWarn'),
            disk_free_critical_pct: numVal('acDiskCrit'),
            cpu_temp_warn_c: numVal('acTempWarn'),
            cpu_temp_critical_c: numVal('acTempCrit'),
            notify_recovery: !!(acFields.acNotifyRecovery && acFields.acNotifyRecovery.checked),
        };
    }

    async function saveAlertConfig() {
        try {
            const payload = collectAlertPayload();
            const res = await fetch('/api/alert-config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || `HTTP ${res.status}`);
            }
            dirty = false;
            showToast(t('config.saved'), 'success');
        } catch (err) {
            showToast(t('config.save_failed') + ': ' + (err && err.message ? err.message : err), 'error');
        }
    }
    if (alertConfigSaveBtn) {
        alertConfigSaveBtn.addEventListener('click', () => guardedSave(saveAlertConfig));
    }
    // アラートの入力変更は「離脱時保存」の対象として記録するだけ。
    if (alertConfigPanel) {
        alertConfigPanel.addEventListener('input', markDirty);
        alertConfigPanel.addEventListener('change', markDirty);
    }


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
            if (!response.ok) {
                // Surface the server's validation message (e.g. a placeholder
                // webhook) instead of the bare status text.
                const err = await response.json().catch(() => ({}));
                throw new Error(err.error || response.statusText || `HTTP ${response.status}`);
            }
            await response.json();
            dirty = false;
            showToast(t('config.saved'), 'success');
        } catch (error) {
            console.error('Failed to save config:', error);
            showToast(t('config.save_failed') + ': ' + (error && error.message ? error.message : error), 'error');
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

    // ============================================================
    // 保存（手動 + 離脱時）
    // ============================================================
    // 入力のたびに自動保存すると、編集中の途中状態が何度も保存されて
    // 意図しない値が確定してしまう。そこで自動保存は「ページを離れる
    // 瞬間」にだけ行い、通常は保存ボタン / Ctrl+S で明示的に保存する。
    // 離脱時は fetch の keepalive でページ遷移後も送信を継続させる。
    let saving = false;
    let saveQueued = false;
    // dirty は「保存していない変更があるか」。離脱時に保存するかの判断に使う。
    let dirty = false;
    function markDirty() { dirty = true; }

    function setSavingLabel(on) {
        // フッターの文字数表示の隣に「保存中… / 自動保存済み」を出す（任意）。
        if (!validationStatus) return;
        const st = validationStatus.querySelector('.status-text');
        if (!st) return;
        if (on) {
            st.dataset.prevText = st.textContent;
            st.textContent = t('config.saving');
        } else if (st.dataset.prevText) {
            st.textContent = st.dataset.prevText;
            delete st.dataset.prevText;
        }
    }

    // 現在の設定タイプに応じて保存関数を選ぶ。
    // async にしてはいけない: 呼び出し側は `guardedSave(currentSaveFn())` と
    // 書いており、async だと Promise が fn に渡って `fn is not a function`
    // になる（ここは関数を「返す」だけで、実行は guardedSave が行う）。
    function currentSaveFn() {
        if (currentConfigType === 'alerts') return saveAlertConfig;
        if (currentMode === 'gui') return saveGui;
        return saveConfig;
    }

    // 離脱時に保存すべき内容（URL と本文）を組み立てる。保存不可なら null。
    function buildLeaveSave() {
        const type = currentConfigType;
        if (type === 'alerts') {
            return { url: '/api/alert-config', body: JSON.stringify(collectAlertPayload()) };
        }
        if (currentMode === 'gui') {
            return { url: `/api/config/${type}`, body: JSON.stringify(collectGui()) };
        }
        // JSON モード: 構文が壊れているときは送らない。
        try { JSON.parse(configEditor.value); } catch (e) { return null; }
        return { url: `/api/config/${type}`, body: configEditor.value };
    }

    // saveOnLeave はページ離脱の瞬間にだけ保存する。fetch の keepalive を
    // 使うと、ページがアンロードされた後もリクエストが送信される。
    // （sendBeacon は POST のみで PUT の API に使えないため fetch を使う。）
    function saveOnLeave() {
        if (!dirty || saving) return;
        const req = buildLeaveSave();
        if (!req) return;
        try {
            fetch(req.url, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: req.body,
                keepalive: true,
            });
            dirty = false;
        } catch (e) { /* 離脱時は失敗しても通知できない */ }
    }

    // pagehide はアンロード/バックフォワードキャッシュ退避の両方で発火し、
    // beforeunload より確実（モバイル Safari では beforeunload が来ない）。
    // visibilitychange(hidden) はタブ切替・アプリ切替でも保存でき、離脱前に
    // 確実に走る最後の砦。両方から呼ぶが dirty フラグで二重送信を防ぐ。
    window.addEventListener('pagehide', saveOnLeave);
    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'hidden') saveOnLeave();
    });

    // guardedSave は保存の多重実行を防ぐ。手動保存は saving 中でも予約して
    // 最後に1回実行される（入力の取りこぼしを防ぐ）。
    async function guardedSave(fn, silent) {
        if (saving) { saveQueued = true; return; }
        saving = true;
        if (!silent) setSavingLabel(true);
        try {
            await fn();
        } catch (err) {
            // Safety net: a save function must not fail silently. Each save
            // function already shows its own toast, but an unexpected throw
            // here (e.g. a DOM issue) still needs to be visible.
            console.error('save failed:', err);
            showToast(t('config.save_failed') + ': ' + (err && err.message ? err.message : err), 'error');
        } finally {
            saving = false;
            if (!silent) setSavingLabel(false);
            if (saveQueued) {
                saveQueued = false;
                // 予約分を1回だけ実行（無限ループ防止のため直接呼ぶ）。
                try { await fn(); } catch (e) { /* 各 save 関数は内部で通知する */ }
            }
        }
    }

    // ============================================================
    // 設定の履歴（スナップショット / タイムトラベル）
    // ============================================================
    // サーバーは保存のたびに直前の内容を snapshots/ に退避している。
    // 履歴ボタンで一覧を出し、任意の時点に復元できる。
    // アラート（alerts）は /api/config の対象外なので履歴は出さない。
    function historyAvailable() {
        return currentConfigType === 'agent' || currentConfigType === 'dashboard';
    }

    function openHistoryModal() {
        if (!historyModal) return;
        historyModal.style.display = 'flex';
        renderHistory();
    }

    function closeHistoryModal() {
        if (historyModal) historyModal.style.display = 'none';
    }

    async function renderHistory() {
        if (!historyList) return;
        historyList.textContent = '';
        const loading = document.createElement('div');
        loading.className = 'history-empty';
        loading.textContent = t('config.loading');
        historyList.appendChild(loading);

        try {
            const res = await fetch(`/api/config/${currentConfigType}/snapshots`);
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const data = await res.json();
            const snaps = data.snapshots || [];
            historyList.textContent = '';
            if (snaps.length === 0) {
                const empty = document.createElement('div');
                empty.className = 'history-empty';
                empty.textContent = t('config.history_empty');
                historyList.appendChild(empty);
                return;
            }
            snaps.forEach((s) => historyList.appendChild(buildHistoryRow(s)));
        } catch (err) {
            historyList.textContent = '';
            const e = document.createElement('div');
            e.className = 'history-empty';
            e.textContent = t('config.history_load_failed') + ': ' + (err && err.message ? err.message : err);
            historyList.appendChild(e);
        }
    }

    function buildHistoryRow(snap) {
        const row = document.createElement('div');
        row.className = 'history-row';

        const info = document.createElement('div');
        info.className = 'history-info';
        const when = document.createElement('div');
        when.className = 'history-time';
        when.textContent = formatSnapTime(snap.time);
        const size = document.createElement('div');
        size.className = 'history-size';
        size.textContent = formatBytes(snap.size);
        info.appendChild(when);
        info.appendChild(size);

        const btn = document.createElement('button');
        btn.className = 'btn-secondary history-restore';
        btn.textContent = t('config.restore');
        btn.addEventListener('click', () => restoreSnapshot(snap.name));

        row.appendChild(info);
        row.appendChild(btn);
        return row;
    }

    function formatSnapTime(iso) {
        const d = new Date(iso);
        if (isNaN(d.getTime())) return iso;
        const p = (n) => String(n).padStart(2, '0');
        return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
    }

    function formatBytes(n) {
        n = Number(n) || 0;
        if (n < 1024) return n + ' B';
        if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
        return (n / 1024 / 1024).toFixed(2) + ' MB';
    }

    async function restoreSnapshot(name) {
        if (!window.confirm(t('config.restore_confirm'))) return;
        try {
            const res = await fetch(`/api/config/${currentConfigType}/restore`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name }),
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || `HTTP ${res.status}`);
            }
            showToast(t('config.restored'), 'success');
            closeHistoryModal();
            // 復元後の内容を読み直す。
            if (currentConfigType !== 'alerts') loadConfig(currentConfigType);
        } catch (err) {
            showToast(t('config.restore_failed') + ': ' + (err && err.message ? err.message : err), 'error');
        }
    }

    if (historyBtn) historyBtn.addEventListener('click', openHistoryModal);
    if (historyCloseBtn) historyCloseBtn.addEventListener('click', closeHistoryModal);
    if (historyModal) {
        historyModal.addEventListener('click', (e) => {
            if (e.target === historyModal) closeHistoryModal();
        });
    }

    function showToast(message, type = 'info') {
        const container = document.getElementById('toastContainer');
        if (!container) return;
        // 連打などで同じ内容のトーストが積み上がらないよう、既存の同一トーストは
        // 先に消してから出し直す。
        const key = type + '\u0000' + message;
        Array.from(container.children).forEach((c) => {
            if (c.dataset.toastKey === key) c.remove();
        });
        // 表示は最大 MAX_TOASTS 件まで。あふれた分は古い順に消す。
        const MAX_TOASTS = 4;
        while (container.children.length >= MAX_TOASTS) {
            container.removeChild(container.firstChild);
        }
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.dataset.toastKey = key;
        toast.textContent = message;
        container.appendChild(toast);
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
    if (saveBtn) saveBtn.addEventListener('click', () => guardedSave(currentSaveFn()));
    // 履歴ボタンはアラート以外でのみ有効。
    if (historyBtn) {
        historyBtn.addEventListener('click', () => {
            if (!historyAvailable()) {
                showToast(t('config.history_unavailable'), 'info');
                return;
            }
        });
    }

    configEditor.addEventListener('input', () => {
        updateCharCount();
        try { JSON.parse(configEditor.value); updateValidationStatus(true); }
        catch (error) { updateValidationStatus(false); }
        // 変更を記録するだけ（実際の保存は保存ボタン / Ctrl+S / 離脱時）。
        markDirty();
    });

    // Ctrl/Cmd+S はグローバルに効かせる（エディタ外にフォーカスがあっても保存）。
    document.addEventListener('keydown', (e) => {
        if ((e.ctrlKey || e.metaKey) && !e.shiftKey && (e.key === 's' || e.key === 'S')) {
            e.preventDefault();
            guardedSave(currentSaveFn());
            return;
        }
        if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'F' || e.key === 'f')) {
            e.preventDefault();
            formatJSON();
        }
    });

    window.addEventListener('kizuna-lang-change', () => {
        setTheme(getTheme());
        updateCharCount();
        updateValidationStatus(true);
        if (currentConfigType !== 'alerts' && currentMode === 'gui') renderGui();
    });

    // 初期読み込み
    updateModeButtons();
    loadConfig(currentConfigType);
});
