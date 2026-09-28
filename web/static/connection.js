// ============================================================
// Kizuna-Eye - 接続ステータス表示（ダッシュボード以外のページ用）
// WebSocket の接続状態を #connectionStatus バッジに反映する。
// ============================================================
(function () {
    'use strict';

    const el = document.getElementById('connectionStatus');
    if (!el) return;

    const t = (key) => (window.KizunaI18n ? window.KizunaI18n.t(key) : key);

    let socket = null;
    let reconnectTimer = null;
    let reconnectAttempts = 0;
    const RECONNECT_DELAY = 3000;
    let currentState = 'connecting';

    function setState(state) {
        currentState = state;
        el.classList.remove('connected', 'disconnected', 'connecting');
        el.classList.add(state);
        switch (state) {
            case 'connected':    el.textContent = t('status.connected');    break;
            case 'connecting':   el.textContent = t('status.connecting');   break;
            case 'disconnected': el.textContent = t('status.disconnected'); break;
            default:             el.textContent = state;
        }
    }

    function connect() {
        if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
            return;
        }

        setState('connecting');

        const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        try {
            socket = new WebSocket(`${protocol}//${location.host}/ws`);
        } catch (e) {
            setState('disconnected');
            scheduleReconnect();
            return;
        }

        socket.addEventListener('open', () => {
            reconnectAttempts = 0;
            setState('connected');
        });

        socket.addEventListener('close', () => {
            setState('disconnected');
            scheduleReconnect();
        });

        socket.addEventListener('error', () => {
            // close イベントで処理するためここでは何もしない
        });
    }

    function scheduleReconnect() {
        if (reconnectTimer) return;
        reconnectAttempts++;
        const delay = Math.min(RECONNECT_DELAY * reconnectAttempts, 15000);
        reconnectTimer = setTimeout(() => {
            reconnectTimer = null;
            connect();
        }, delay);
    }

    function init() {
        setState('connecting');
        connect();
        // 言語切替時に現在の状態を再描画
        window.addEventListener('kizuna-lang-change', () => setState(currentState));
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
