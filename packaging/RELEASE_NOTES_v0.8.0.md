# Kizuna-Eye v0.8.0 — Release Notes

Released: 2026-10-13

## Highlights / ハイライト

- **Plugin ABI safety**: host and every plugin must be built under one combined go.work with -trimpath, so a host-only rebuild cannot break plugin.Open with "different version of package". build-all.sh makes this the default path.
- **Security hardening**: avatar serving no longer follows symlinks (CWE-59), the privileged action helper resolves the sudo binary by absolute path, and the plugin inspector runs under bubblewrap by default (fail-closed).
- **Secure defaults for new installs**: install.sh and the .deb enable authentication (auth.enabled: true, public_viewer: false) on a brand-new config. Existing installs are left unchanged.
- **One-command install**: apt install kizuna-eye performs the whole setup (user, config, systemd, action socket).
- **Bilingual comments**: every comment is now plain Japanese + English, enforced by scripts/check_comments.cjs in CI.

---

## 日本語

### 主な変更

- **プラグイン ABI の安全化**: ホストと全プラグインを 1 つの combined go.work の下、-trimpath 付きでビルド。ホストだけ再ビルドしても plugin.Open が壊れない。
- **セキュリティ強化**: アバター配信の symlink 追従を廃止（CWE-59）、特権ヘルパーの sudo を絶対パス解決、プラグイン検査を既定で bubblewrap 分離。
- **新規インストールのセキュア既定**: install.sh / .deb は新規設定で認証を有効化。既存環境は変更しない。
- **apt 一発導入**: apt install kizuna-eye だけでセットアップ完結。
- **コメントの日英バイリンガル化**: コード全体を「日本語（常体）→英語」に統一、CI で回帰検出。

### 破壊的変更

- なし（config_version により既存設定はそのまま動作）。

### 対応環境

- Ubuntu Server 20.04 LTS 以降 / Debian 11 以降（amd64）。

---

## English

### Highlights

- **Plugin ABI safety**: build the host and every plugin under one combined go.work with -trimpath.
- **Security hardening**: avatar serving refuses symlinks (CWE-59); privileged helper resolves sudo by absolute path; plugin inspection runs under bubblewrap by default.
- **Secure defaults for new installs**; existing installs are untouched.
- **One-command install**; **bilingual comments** enforced in CI.

### Breaking changes

- None (config_version keeps existing configs working).

### Supported

- Ubuntu Server 20.04 LTS+ / Debian 11+ (amd64).

See CHANGELOG.md for the full list of changes.
