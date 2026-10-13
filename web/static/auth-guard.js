// ============================================================
// Kizuna-Eye - ロールに応じたナビ表示制御とアカウントメニュー
// 各ページの </body> 直前で読み込む。
// - /api/auth/me で現在のユーザーとロールを取得
// - data-role-min を持つタブのうち、権限が足りないものを非表示
// - ヘッダー右上にアバターを表示し、クリックでアカウントメニューを開く
// ============================================================
(function () {
    'use strict';

    const ROLE_RANK = { viewer: 1, operator: 2, admin: 3 };

    function rank(role) {
        return ROLE_RANK[role] || 0;
    }

    // If auth is disabled, /api/auth/me returns 401; in that case do nothing
    // so the dashboard keeps working as before.
    fetch('/api/auth/me', { credentials: 'same-origin' })
        .then(function (r) {
            if (!r.ok) return null;
            return r.json();
        })
        .then(function (me) {
            if (!me) return; // auth disabled or not logged in
            applyRole(me);
        })
        .catch(function () {});

    function applyRole(me) {
        const myRank = rank(me.role);

        // Hide nav tabs the user cannot access.
        document.querySelectorAll('[data-role-min]').forEach(function (el) {
            const need = rank(el.getAttribute('data-role-min'));
            if (myRank < need) {
                el.style.display = 'none';
            }
        });

        // Place the avatar menu in the first header row (right side), next to
        // the other controls.
        const anchor = document.querySelector('.header-controls')
            || document.getElementById('headerUser')
            || document.querySelector('.header-right');
        if (!anchor) return;
        buildAvatarMenu(anchor, me);
    }

    // buildAvatarMenu renders an avatar button that opens a dropdown with
    // account information and actions.
    function buildAvatarMenu(anchor, me) {
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
        nameEl.textContent = me.username;
        const roleEl = document.createElement('div');
        roleEl.className = 'account-role';
        roleEl.textContent = me.role;
        info.appendChild(nameEl);
        info.appendChild(roleEl);
        head.appendChild(info);
        dropdown.appendChild(head);

        // --- account settings (avatar upload / password) ---
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

        settings.appendChild(menuItem('🖼️ アイコンを変更', function () { fileInput.click(); }));
        settings.appendChild(menuItem('🔑 パスワードを変更', function () { changeOwnPassword(); }));
        if (me.role === 'admin') {
            const usersLink = document.createElement('a');
            usersLink.className = 'account-item';
            usersLink.href = '/users.html';
            usersLink.textContent = '👤 ユーザー管理';
            settings.appendChild(usersLink);
        }
        dropdown.appendChild(settings);
        dropdown.appendChild(fileInput);

        // --- logout ---
        const sep = document.createElement('div');
        sep.className = 'account-sep';
        dropdown.appendChild(sep);
        dropdown.appendChild(menuItem('↪ ログアウト', function () {
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
        anchor.appendChild(wrap);
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
    // Cache-bust with the file name so a freshly uploaded avatar replaces the
    // previous image immediately.
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
