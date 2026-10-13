// ============================================================
// Kizuna-Eye - ロールに応じたナビ表示制御とアカウントメニュー
// 各ページの </body> 直前で読み込む。
// - /api/auth/status で現在の状態を取得
// - data-role-min を持つ要素のうち、権限が足りないものを非表示
// - ログイン済み（ゲスト含む）: 右上にアバター + ログアウトメニュー
// - 未ログイン（公開ビューア）: 右上に「ログイン」リンク
// Kizuna-Eye - role-based nav visibility and account menu
// Loaded just before </body> on each page.
// - Fetch the current state from /api/auth/status
// - Hide elements with data-role-min when the role is insufficient
// - Logged in (incl. guest): avatar + logout menu at top right
// - Not logged in (public viewer): a "Log in" link at top right
// ============================================================
(function () {
    'use strict';

    const ROLE_RANK = { viewer: 1, operator: 2, admin: 3 };

    function rank(role) {
        return ROLE_RANK[role] || 0;
    }

    // i18n 翻訳ヘルパー
    // i18n translation helper
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
            if (!st) return;                 // 判定失敗 / lookup failed
            if (st.auth_enabled === false) return; // 認証無効: 何もしない / auth disabled: do nothing
            if (!st.authenticated) {
                // 公開ビューア（未ログイン）: ログイン導線を出す。
                // Public viewer (not logged in): show the login entry point.
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

    // insertIntoHeader はヘッダへ要素を配置する。version バッジがあればその前に
    // 挿入し、順序を [lang][theme][user][version] にする。
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

    // addLoginLink は公開 viewer がログインページ（そしてゲストログイン /
    // ログアウト）へ到達できるようログイン入口を追加する。
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
        // If the page-wide minimum role (<body data-role-min>) is not met,
        // send the user back to the dashboard.
        const pageMin = document.body && document.body.getAttribute('data-role-min');
        if (pageMin && myRank < rank(pageMin)) {
            location.replace('/');
            return;
        }

        // アクセスできないナビタブを隠す。
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

    // buildAvatarMenu は、アカウント情報と操作を含むドロップダウンを開く
    // アバターボタンを描画する。ゲストアカウントはログアウトのみだ。
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
        // Guests have no credentials, so do not show these.
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
            if (open) {
                clampDropdown(dropdown);
            }
        });

        wrap.appendChild(btn);
        wrap.appendChild(dropdown);
        insertIntoHeader(anchor, wrap);
    }

    // clampDropdown は絶対配置のドロップダウンを「両端」でビューポート内に保つ。
    // ドロップダウンはアバターに right:0 で固定される。狭いスマホではアバターが
    // 左端近くに来ることがあり（ヘッダが折り返す）、260px のメニューが左端を
    // 超える。右端近くでも右を超え得る。そこで最終ボックスを
    // [margin, innerWidth-margin] にクランプする。既定の right:0 がビューポートを
    // 外すときはアンカーを反転し、幅も制限する。ちょうど hidden 解除された要素が
    // レイアウトされるよう rAF で計測する。
    // clampDropdown keeps an absolutely-positioned dropdown inside the
    // viewport on BOTH edges. The dropdown is anchored right:0 to the avatar;
    // on a narrow phone the avatar can sit near the LEFT edge (the header
    // wraps) and a 260px menu reaches past the left edge. If it also sits
    // near the RIGHT edge it can reach past the right. We therefore clamp the
    // final box to [margin, innerWidth-margin]: flip the anchor when the
    // default right:0 position would leave the viewport, and cap the width.
    // Measured in rAF so the just-un-hidden element has been laid out.
    function clampDropdown(dropdown) {
        // まず既定のアンカーへ戻し、繰り返し開いてもオフセットが蓄積しない
        // ようにする。
        // Reset to the default anchor first so repeated opens do not
        // accumulate offsets.
        dropdown.style.left = '';
        dropdown.style.right = '';
        dropdown.style.maxWidth = '';
        requestAnimationFrame(function () {
            const margin = 8;
            const vw = window.innerWidth || document.documentElement.clientWidth;
            // ドロップダウンは .account-menu（最も近い位置指定祖先 = アバターボタン）
            // を基準に配置される。左/右の反転は（アバターが狭い画面の中央にあると
            // どちらの端も広いメニューに収まらず）失敗するため、代わりにメニュー幅を
            // ビューポートに収まるよう計算し、アバターからの明示的な左オフセットで
            // 配置して常に [margin, vw-margin] 内に着地させる。
            // The dropdown is positioned relative to .account-menu (the
            // nearest positioned ancestor = the avatar button). Rather than
            // flipping left/right (which fails when the avatar sits in the
            // MIDDLE of a narrow screen, because neither edge fits a wide
            // menu), compute the menu width to fit the viewport and place it
            // with an explicit left offset relative to the avatar so it
            // always lands inside [margin, vw-margin].
            const anchor = dropdown.parentElement &&
                dropdown.parentElement.querySelector('.account-avatar');
            if (!anchor) return;
            const b = anchor.getBoundingClientRect();
            // 希望幅: 自然な min-width（260px）を優先しつつ、ビューポートより
            // 広くしない。
            // Desired width: prefer the natural min-width (260px) but never
            // wider than the viewport.
            let width = 260;
            if (dropdown.scrollWidth > width) width = dropdown.scrollWidth;
            const maxW = vw - margin * 2;
            if (width > maxW) width = maxW;
            dropdown.style.width = width + 'px';
            dropdown.style.maxWidth = maxW + 'px';
            // 既定ではアバターの下に右揃えし、左端をビューポート内へクランプする。
            // それで右端が外れるなら、ボックス全体が画面に残るよう戻す。
            // Right-align under the avatar by default, then clamp the LEFT
            // edge into the viewport; if that pushes the right edge out,
            // shift back so the whole box stays on screen.
            let desiredLeft = b.right - width;
            if (desiredLeft < margin) desiredLeft = margin;
            if (desiredLeft + width > vw - margin) desiredLeft = vw - margin - width;
            // 位置指定祖先を基準のオフセットへ変換する。
            // Convert to an offset relative to the positioned ancestor.
            const offset = desiredLeft - b.left;
            dropdown.style.right = 'auto';
            dropdown.style.left = offset + 'px';
        });
    }

    // menuItem はボタンを裏に持つドロップダウン行を作る。
    // menuItem builds a dropdown row backed by a button.
    function menuItem(label, onClick) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'account-item';
        b.textContent = label;
        b.addEventListener('click', function (e) { e.stopPropagation(); onClick(); });
        return b;
    }

    // renderAvatar は要素をユーザーのアバター画像またはイニシャルで埋める。
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

    // uploadAvatar は選択された画像を送り、アバターを更新する。
    // uploadAvatar sends the selected image and refreshes the avatar.
    function uploadAvatar(file, wrap, me) {
        const fd = new FormData();
        fd.append('avatar', file);
        fetch('/api/auth/avatar', { method: 'POST', credentials: 'same-origin', body: fd })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || t('account.err_upload', 'Upload failed')); return; }
                me.avatar = res.body.avatar;
                wrap.querySelectorAll('.account-avatar').forEach(function (el) { renderAvatar(el, me); });
            })
            .catch(function () { alert(t('account.err_network', 'Network error')); });
    }

    // changeOwnPassword は現在と新しいパスワードを尋ねる。
    // changeOwnPassword prompts for the current and new password.
    function changeOwnPassword() {
        const current = prompt(t('account.prompt_current_pw', 'Current password'));
        if (current === null) return;
        const next = prompt(t('account.prompt_new_pw', 'New password (at least 8 characters)'));
        if (next === null) return;
        fetch('/api/auth/password', {
            method: 'PUT', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ current_password: current, new_password: next })
        })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || t('account.err_change', 'Change failed')); return; }
                alert(t('account.pw_changed_relogin', 'Password changed. Please log in again.'));
                location.replace('/login.html');
            })
            .catch(function () { alert(t('account.err_network', 'Network error')); });
    }

    // closeAllAccountMenus は開いている全ドロップダウンを隠す。
    // closeAllAccountMenus hides every open dropdown.
    function closeAllAccountMenus() {
        document.querySelectorAll('.account-dropdown').forEach(function (d) { d.hidden = true; });
        document.querySelectorAll('.account-avatar[aria-expanded]').forEach(function (b) { b.setAttribute('aria-expanded', 'false'); });
    }

    // 外側クリックまたは Escape で閉じる。
    // Close on outside click or Escape.
    document.addEventListener('click', function () { closeAllAccountMenus(); });
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') closeAllAccountMenus();
    });
})();
