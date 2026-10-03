// Kizuna-Eye - 初回セットアップ処理
(function () {
    'use strict';

    const form = document.getElementById('setupForm');
    const btn = document.getElementById('setupBtn');
    const errEl = document.getElementById('authError');

    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    function showError(msg) {
        if (!errEl) return;
        errEl.textContent = msg;
        errEl.hidden = false;
    }

    // If setup is already done, send the user to login.
    fetch('/api/auth/status', { credentials: 'same-origin' })
        .then(function (r) { return r.json(); })
        .then(function (s) {
            if (s && s.auth_enabled === false) {
                // Auth disabled: setup is not applicable.
                location.replace('/');
                return;
            }
            if (s && !s.needs_setup) {
                location.replace('/login.html');
            }
        })
        .catch(function () {});

    if (form) {
        form.addEventListener('submit', function (e) {
            e.preventDefault();
            if (errEl) errEl.hidden = true;

            const username = document.getElementById('username').value.trim();
            const password = document.getElementById('password').value;
            const password2 = document.getElementById('password2').value;

            if (password !== password2) {
                showError(t('setup.err_mismatch'));
                return;
            }
            if (password.length < 8) {
                showError(t('setup.err_short'));
                return;
            }

            btn.disabled = true;
            fetch('/api/auth/setup', {
                method: 'POST',
                credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username: username, password: password })
            })
                .then(function (r) {
                    return r.json().then(function (body) { return { ok: r.ok, body: body }; });
                })
                .then(function (res) {
                    if (res.ok) {
                        location.replace('/login.html');
                        return;
                    }
                    btn.disabled = false;
                    showError((res.body && res.body.error) || t('setup.err_failed'));
                })
                .catch(function () {
                    btn.disabled = false;
                    showError(t('setup.err_network'));
                });
        });
    }
})();
