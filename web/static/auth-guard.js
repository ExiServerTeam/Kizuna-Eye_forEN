// ============================================================
// Kizuna-Eye - ロールに応じたナビ表示制御とアカウントメニュー
// 各ページの </body> 直前で読み込む。
// - /api/auth/status で現在の状態を取得
// - data-role-min を持つ要素のうち、権限が足りないものを非表示
// - ログイン済み（ゲスト含む）: 右上にアバター + ログアウトメニュー
// - 未ログイン（公開ビューア）: 右上に「ログイン」リンク
// ============================================================
(function () {
    'use strict';

    const ROLE_RANK = { viewer: 1, operator: 2, admin: 3 };

    function rank(role) {
        return ROLE_RANK[role] || 0;
    }

    // i18n 翻訳ヘルパー
    function t(key, fallback) {
        if (window.KizunaI18n && window.KizunaI18n.t) {
            const v = window.KizunaI18n.t(key);
            if (v && v !== key) return v;
        }
        return fallback;
    }

    fetch('/api/auth/status', { credentials: 'same-origin' })
        .then(function (r) {
            if (!r.ok) return null;
            return r.json();
        })
        .then(function (st) {
            if (!st) return;                 // 判定失敗
            if (st.auth_enabled === false) return; // 認証無効: 何もしない
            if (!st.authenticated) {
                // 公開ビューア（未ログイン）: ログイン導線を出す。
                addLoginLink();
                return;
            }
            applyRole({ username: st.username, role: st.role, avatar: st.avatar });
        })
        .catch(function () {});

    function headerAnchor() {
        return document.querySelector('.header-controls')
            || document.getElementById('headerUser')
            || document.querySelector('.header-right');
    }

    // insertIntoHeader places an element in the header. When a version badge
    // exists, insert before it so the order is [lang][theme][user][version].
    function insertIntoHeader(anchor, el) {
        const badge = anchor.querySelector('.version-badge');
        if (badge) {
            anchor.insertBefore(el, badge);
        } else {
            anchor.appendChild(el);
        }
    }

    // addLoginLink adds a login entry so a public viewer can reach the
    // login page (and then guest-login / log out).
    function addLoginLink() {
        const anchor = headerAnchor();
        if (!anchor) return;
        if (document.getElementById('headerLoginLink')) return;
        const a = document.createElement('a');
        a.id = 'headerLoginLink';
        a.className = 'header-login-link';
        a.href = '/login.html';
        a.textContent = t('nav.login', 'ログイン');
        insertIntoHeader(anchor, a);
    }

    function applyRole(me) {
        const myRank = rank(me.role);

        // ページ全体に必要な権限（<body data-role-min>）を満たさない場合は
        // ダッシュボードへ戻す。
        const pageMin = document.body && document.body.getAttribute('data-role-min');
        if (pageMin && myRank < rank(pageMin)) {
            location.replace('/');
            return;
        }

        // Hide nav tabs the user cannot access.
        document.querySelectorAll('[data-role-min]').forEach(function (el) {
            const need = rank(el.getAttribute('data-role-min'));
            if (myRank < need) {
                el.style.display = 'none';
            }
        });

        const anchor = headerAnchor();
        if (!anchor) return;
        buildAvatarMenu(anchor, me);
    }

    // buildAvatarMenu renders an avatar button that opens a dropdown with
    // account information and actions. Guest accounts only get logout.
    function buildAvatarMenu(anchor, me) {
        const isGuest = me.username === 'guest';

        const wrap = document.createElement('div');
        wrap.className = 'account-menu';

        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'account-avatar';
        btn.setAttribute('aria-haspopup', 'true');
        btn.setAttribute('aria-expanded', 'false');
        btn.setAttribute('aria-label', me.username);
        renderAvatar(btn, me);

        const dropdown = document.createElement('div');
        dropdown.className = 'account-dropdown';
        dropdown.hidden = true;

        // --- header: avatar + name + role ---
        const head = document.createElement('div');
        head.className = 'account-head';
        const headAvatar = document.createElement('div');
        headAvatar.className = 'account-avatar account-avatar-lg';
        renderAvatar(headAvatar, me);
        head.appendChild(headAvatar);
        const info = document.createElement('div');
        info.className = 'account-info';
        const nameEl = document.createElement('div');
        nameEl.className = 'account-name';
        nameEl.textContent = isGuest ? t('account.guest', 'ゲスト') : me.username;
        const roleEl = document.createElement('div');
        roleEl.className = 'account-role';
        roleEl.textContent = me.role;
        info.appendChild(nameEl);
        info.appendChild(roleEl);
        head.appendChild(info);
        dropdown.appendChild(head);

        // --- account settings (avatar upload / password) ---
        // ゲストは資格情報を持たないので、これらは表示しない。
        if (!isGuest) {
            const settings = document.createElement('div');
            settings.className = 'account-section';

            const fileInput = document.createElement('input');
            fileInput.type = 'file';
            fileInput.accept = 'image/png,image/jpeg,image/gif,image/webp';
            fileInput.hidden = true;
            fileInput.addEventListener('change', function () {
                if (fileInput.files && fileInput.files[0]) {
                    uploadAvatar(fileInput.files[0], wrap, me);
                }
                fileInput.value = '';
            });

            settings.appendChild(menuItem(t('account.change_icon', 'アイコンを変更'), function () { fileInput.click(); }));
            settings.appendChild(menuItem(t('account.change_pw', 'パスワードを変更'), function () { changeOwnPassword(); }));
            if (me.role === 'admin') {
                const usersLink = document.createElement('a');
                usersLink.className = 'account-item';
                usersLink.href = '/users.html';
                usersLink.textContent = t('nav.users', 'ユーザー管理');
                settings.appendChild(usersLink);
            }
            dropdown.appendChild(settings);
            dropdown.appendChild(fileInput);
        }

        // --- logout ---
        const sep = document.createElement('div');
        sep.className = 'account-sep';
        dropdown.appendChild(sep);
        dropdown.appendChild(menuItem(t('account.logout', 'ログアウト'), function () {
            fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' })
                .finally(function () { location.replace('/login.html'); });
        }));

        btn.addEventListener('click', function (e) {
            e.stopPropagation();
            const open = dropdown.hidden;
            closeAllAccountMenus();
            dropdown.hidden = !open;
            btn.setAttribute('aria-expanded', String(open));
        });

        wrap.appendChild(btn);
        wrap.appendChild(dropdown);
        insertIntoHeader(anchor, wrap);
    }

    // menuItem builds a dropdown row backed by a button.
    function menuItem(label, onClick) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'account-item';
        b.textContent = label;
        b.addEventListener('click', function (e) { e.stopPropagation(); onClick(); });
        return b;
    }

    // renderAvatar fills an element with the user's avatar image or initials.
    function renderAvatar(el, me) {
        while (el.firstChild) el.removeChild(el.firstChild);
        if (me.avatar) {
            const img = document.createElement('img');
            img.src = '/api/auth/avatar/' + encodeURIComponent(me.avatar);
            img.alt = '';
            img.addEventListener('error', function () {
                while (el.firstChild) el.removeChild(el.firstChild);
                el.textContent = initials(me.username);
            });
            el.appendChild(img);
        } else {
            el.textContent = initials(me.username);
        }
    }

    function initials(name) {
        return (name || '?').slice(0, 1).toUpperCase();
    }

    // uploadAvatar sends the selected image and refreshes the avatar.
    function uploadAvatar(file, wrap, me) {
        const fd = new FormData();
        fd.append('avatar', file);
        fetch('/api/auth/avatar', { method: 'POST', credentials: 'same-origin', body: fd })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || 'アップロードに失敗しました'); return; }
                me.avatar = res.body.avatar;
                wrap.querySelectorAll('.account-avatar').forEach(function (el) { renderAvatar(el, me); });
            })
            .catch(function () { alert('通信エラー'); });
    }

    // changeOwnPassword prompts for the current and new password.
    function changeOwnPassword() {
        const current = prompt('現在のパスワード');
        if (current === null) return;
        const next = prompt('新しいパスワード（8文字以上）');
        if (next === null) return;
        fetch('/api/auth/password', {
            method: 'PUT', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ current_password: current, new_password: next })
        })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || '変更に失敗しました'); return; }
                alert('パスワードを変更しました。再度ログインしてください。');
                location.replace('/login.html');
            })
            .catch(function () { alert('通信エラー'); });
    }

    // closeAllAccountMenus hides every open dropdown.
    function closeAllAccountMenus() {
        document.querySelectorAll('.account-dropdown').forEach(function (d) { d.hidden = true; });
        document.querySelectorAll('.account-avatar[aria-expanded]').forEach(function (b) { b.setAttribute('aria-expanded', 'false'); });
    }

    // Close on outside click or Escape.
    document.addEventListener('click', function () { closeAllAccountMenus(); });
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') closeAllAccountMenus();
    });
})();
