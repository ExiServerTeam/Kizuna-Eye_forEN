// Kizuna-Eye - ログイン処理
(function () {
    'use strict';

    const form = document.getElementById('loginForm');
    const btn = document.getElementById('loginBtn');
    const errEl = document.getElementById('authError');

    function showError(msg) {
        if (!errEl) return;
        errEl.textContent = msg;
        errEl.hidden = false;
    }

    // Already logged in? Go straight to the dashboard.
    fetch('/api/auth/status', { credentials: 'same-origin' })
        .then(function (r) { return r.json(); })
        .then(function (s) {
            if (!s || s.auth_enabled === false) {
                // Auth disabled: no login needed.
                location.replace('/');
                return;
            }
            if (s.authenticated) {
                location.replace('/');
            } else if (s.needs_setup) {
                location.replace('/setup.html');
            }
        })
        .catch(function () {});

    if (form) {
        form.addEventListener('submit', function (e) {
            e.preventDefault();
            if (errEl) errEl.hidden = true;

            const username = document.getElementById('username').value.trim();
            const password = document.getElementById('password').value;
            if (!username || !password) {
                showError('ユーザー名とパスワードを入力してください');
                return;
            }

            btn.disabled = true;
            fetch('/api/auth/login', {
                method: 'POST',
                credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username: username, password: password })
            })
                .then(function (r) {
                    return r.json().then(function (body) { return { ok: r.ok, status: r.status, body: body }; });
                })
                .then(function (res) {
                    if (res.ok) {
                        location.replace('/');
                        return;
                    }
                    btn.disabled = false;
                    showError((res.body && res.body.error) || 'ログインに失敗しました');
                })
                .catch(function () {
                    btn.disabled = false;
                    showError('通信エラーが発生しました');
                });
        });
    }
})();
