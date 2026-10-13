// ============================================================
// Kizuna-Eye - ロールに応じたナビ表示制御とログアウト
// 各ページの </body> 直前で読み込む。
// - /api/auth/me で現在のユーザーとロールを取得
// - data-role-min を持つタブのうち、権限が足りないものを非表示
// - ヘッダー右上にユーザー名とログアウトボタンを追加
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

        // Show the user menu (admin only link + logout) in the header.
        const headerRight = document.querySelector('.header-right');
        if (!headerRight) return;

        const box = document.createElement('div');
        box.className = 'user-menu';

        const nameSpan = document.createElement('span');
        nameSpan.className = 'user-name';
        nameSpan.textContent = me.username + ' (' + me.role + ')';
        box.appendChild(nameSpan);

        if (me.role === 'admin') {
            const usersLink = document.createElement('a');
            usersLink.href = '/users.html';
            usersLink.className = 'theme-toggle';
            usersLink.textContent = '👤 ユーザー';
            box.appendChild(usersLink);
        }

        const logoutBtn = document.createElement('button');
        logoutBtn.className = 'theme-toggle';
        logoutBtn.textContent = 'ログアウト';
        logoutBtn.addEventListener('click', function () {
            fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' })
                .finally(function () { location.replace('/login.html'); });
        });
        box.appendChild(logoutBtn);

        headerRight.appendChild(box);
    }
})();
