// Kizuna-Eye - ユーザー管理（管理者のみ）
(function () {
    'use strict';

    const tbody = document.getElementById('usersTableBody');
    const errEl = document.getElementById('usersError');
    const addModal = document.getElementById('addModal');
    const addForm = document.getElementById('addUserForm');
    const addError = document.getElementById('addError');

    // i18n 翻訳ヘルパー
    const t = (key, ...args) => (window.KizunaI18n ? window.KizunaI18n.t(key, ...args) : key);

    function showError(el, msg) {
        if (!el) return;
        el.textContent = msg;
        el.hidden = false;
    }

    function roleClass(role) {
        return 'role-badge role-' + role;
    }

    function load() {
        fetch('/api/users', { credentials: 'same-origin' })
            .then(function (r) {
                if (r.status === 401) { location.replace('/login.html'); throw new Error('unauth'); }
                if (r.status === 403) { throw new Error(t('users.need_admin')); }
                return r.json();
            })
            .then(function (data) {
                render(data.users || []);
            })
            .catch(function (e) {
                if (e.message !== 'unauth') showError(errEl, e.message || t('users.load_failed'));
            });
    }

    function render(users) {
        tbody.innerHTML = '';
        users.forEach(function (u) {
            const tr = document.createElement('tr');

            const tdName = document.createElement('td');
            tdName.textContent = u.username;
            tr.appendChild(tdName);

            const tdRole = document.createElement('td');
            const span = document.createElement('span');
            span.className = roleClass(u.role);
            span.textContent = u.role;
            tdRole.appendChild(span);
            tr.appendChild(tdRole);

            const tdDate = document.createElement('td');
            tdDate.textContent = u.created_at ? new Date(u.created_at).toLocaleString() : '-';
            tr.appendChild(tdDate);

            const tdAct = document.createElement('td');

            const pwBtn = document.createElement('button');
            pwBtn.className = 'btn-sm';
            pwBtn.textContent = t('users.change_pw');
            pwBtn.addEventListener('click', function () { changePassword(u.username); });
            tdAct.appendChild(pwBtn);

            const delBtn = document.createElement('button');
            delBtn.className = 'btn-sm danger';
            delBtn.textContent = t('users.delete');
            delBtn.style.marginLeft = '6px';
            delBtn.addEventListener('click', function () { removeUser(u.username); });
            tdAct.appendChild(delBtn);

            tr.appendChild(tdAct);
            tbody.appendChild(tr);
        });
    }

    function changePassword(username) {
        const pw = prompt(t('users.prompt_new_pw', username));
        if (!pw) return;
        fetch('/api/users/' + encodeURIComponent(username) + '/password', {
            method: 'PUT',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: pw })
        })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || t('users.change_failed')); return; }
                alert(t('users.pw_changed'));
            })
            .catch(function () { alert(t('users.network_error')); });
    }

    function removeUser(username) {
        if (!confirm(t('users.confirm_delete', username))) return;
        fetch('/api/users/' + encodeURIComponent(username), {
            method: 'DELETE',
            credentials: 'same-origin'
        })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { alert((res.body && res.body.error) || t('users.delete_failed')); return; }
                load();
            })
            .catch(function () { alert(t('users.network_error')); });
    }

    // ---- add modal ----
    document.getElementById('addUserBtn').addEventListener('click', function () {
        addModal.hidden = false;
        addError.hidden = true;
        addForm.reset();
    });
    document.getElementById('cancelAddBtn').addEventListener('click', function () {
        addModal.hidden = true;
    });

    addForm.addEventListener('submit', function (e) {
        e.preventDefault();
        addError.hidden = true;
        const username = document.getElementById('newUsername').value.trim();
        const password = document.getElementById('newPassword').value;
        const role = document.getElementById('newRole').value;

        fetch('/api/users', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username, password: password, role: role })
        })
            .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
            .then(function (res) {
                if (!res.ok) { showError(addError, (res.body && res.body.error) || t('users.create_failed')); return; }
                addModal.hidden = true;
                load();
            })
            .catch(function () { showError(addError, t('users.network_error')); });
    });

    load();
})();
