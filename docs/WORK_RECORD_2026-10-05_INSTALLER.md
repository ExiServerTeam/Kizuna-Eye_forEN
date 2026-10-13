# 作業記録: install.sh 一本化 / uninstall.sh / systemd 登録 / 実機検証

- 日付: 2026-10-05
- 対象: install.sh / uninstall.sh / update.sh / safe_update.sh / start.sh / stop.sh / systemd unit / ドキュメント
- 目的: install.sh だけで初期セットアップ（systemd 登録込み）を完結させ、uninstall.sh で撤去可能にし、周辺スクリプトを整理する
- 関連: docs/H1_PLAN.md（H-1 権限分離）, docs/PROCEDURE.md, docs/ROADMAP.md, CHANGELOG.md

---

## 1. 目的と背景

- 以前は「install.sh の後に手動で systemd/migrate-agent-user.sh を叩く」二段構えで、dashboard は systemd 未登録のまま手動起動だった。
- 4 本（install / start / stop / update）で運用できる形を目標に、周辺スクリプトを整理し、install.sh 一本化と uninstall.sh を実装した。

---

## 2. install.sh の改修

### 2.1 systemd 登録（dashboard + agent）を内蔵

- 既定で systemd 登録を行う（`--no-systemd` で手動管理に切替）。
- agent は `systemd/migrate-agent-user.sh` を内部で呼び、専用ユーザー `kizuna-eye`（`kizuna-eye-agent.service`）へ移行。
- dashboard は `systemd/kizuna-dashboard.service` を `__USER__` / `__DIR__` / `__BIN__` / `__DATA__` 置換のうえ導入し、`systemctl enable --now` で起動。
- 旧 unit（`kizuna-eye.service` / `kizuna-agent.service`）は二重起動防止のため disable。
- 順序に注意: `migrate-agent-user.sh` は手動プロセス（dashboard 含む）を停止するため、dashboard unit の起動より先に実行する。

### 2.2 純正プラグインの署名

- プラグインビルド後、署名鍵が無ければ生成し、`plugin-sign -sign-all` で純正プラグインに署名。
- 署名鍵は `SUDO_USER` のホーム基準（`$HOME` をそのまま使うと `/root` 配下になり、後段の migrate が公開鍵を見つけられない）。

---

## 3. uninstall.sh の新規作成

- 既定（データ温存）: systemd 停止/disable → unit / sudoers / ヘルパー / `/etc/kizuna-eye` / sysctl 設定を削除。
- `--purge`: 加えて `/var/lib/kizuna-eye`・`~/.kizuna-eye`・`/opt/kizuna-eye`・repo 生成物・専用ユーザー/グループまで削除。
- `--dry-run`（変更なしで内容確認）、`--yes`（確認スキップ）。
- 対象 unit は `kizuna-eye-agent` / `kizuna-dashboard` / 旧 `kizuna-agent` / 旧 `kizuna-eye`。

---

## 4. 周辺スクリプトの整理

### 4.1 削除（install.sh / migrate-agent-user.sh に一本化）

- `systemd/install-services.sh`
- `systemd/migrate-to-systemd.sh`
- `systemd/kizuna-eye.service`（旧 system service）
- `systemd/kizuna-agent.service`（旧 user 版）

### 4.2 存置

- `systemd/kizuna-eye-agent.service`（H-1 本命）
- `systemd/kizuna-dashboard.service`
- `systemd/migrate-agent-user.sh`
- `systemd/kizuna-watchdog.*`（`--no-systemd` 手動管理時に有用）
- `systemd/setup-coredump.sh` / `60-kizuna-core.conf`

### 4.3 update.sh / safe_update.sh

- systemd 検出を dashboard + agent の両 unit 対応にした（`SYSTEMD_UNITS`）。
- update.sh は再ビルド後にプラグイン再署名（`plugin-sign -sign-all -force`）を実施。
- 再起動・ロールバックは検出した unit で実施。

### 4.4 コメント統一

- Go コード・unit コメント内の旧ユーザー名 `kizuna-agent` を `kizuna-eye` に統一。

---

## 5. ドキュメント

- README.ja.md: 「かんたんセットアップ（`sudo ./install.sh`）」＋アンインストール手順を追加。
- docs/PROCEDURE.md: 1.4 を install.sh 一本フローに全面改訂。systemd 操作（`kizuna-eye-agent` / `kizuna-dashboard`）と手動管理（`--no-systemd`）を明記。
- docs/ROADMAP.md: 層5（権限）を C+ → B、H-1 を「完了」に。P0 を削除し P1〜P3 に整理。
- CHANGELOG.md: `[Unreleased]` に Added / Changed / Fixed を追記。
- 作業記録の統一: `session_part*`（22 件）を `WORK_RECORD_2026-10-03_part*.md` にリネームし、参照元も更新。

---

## 6. 実機検証（192.168.0.233 / Ubuntu 26.04.1 / systemd 259）

### 6.1 検証方法

- `scripts/ssh_run.py` ではなく `ssh` + `sudo -S`（標準入力パスワード）で実行。
- 実機は既に H-1 適用済み（agent は `kizuna-eye-agent.service`）だが、dashboard は手動起動だった。

### 6.2 検証で見つけて直したバグ

| # | 問題 | 原因 | 修正 |
|---|---|---|---|
| 1 | install.sh のビルド失敗 | `. /etc/os-release` が `VERSION` を OS バージョン文字列で上書き | os-release をサブシェルで取得 + 変数を `KVERSION` に |
| 2 | `KVERSION` unbound | log 行が定義より前 | 定義順を修正 |
| 3 | root 実行時に cron/action ヘルパーがスキップ | `[ -z "$SUDO" ]` が root で真 | `id -u -ne 0` を併せて判定 |
| 4 | update.sh の署名鍵が `/root/...` | sudo 実行時 `$HOME=/root` | `SUDO_USER` のホーム基準に |
| 5 | update.sh が dashboard を再起動しない | agent unit しか検出しない | `SYSTEMD_UNITS` で両 unit 対応 |

### 6.3 成功したこと

- `install.sh --no-install`: ビルド → プラグイン → 署名 → sudoers → systemd 登録 → 起動。
- 最終状態: `kizuna-eye-agent` / `kizuna-dashboard` とも active + enabled、`/health` = 200。
- `update.sh --force-rebuild`: 再ビルド → 再署名 → 両 unit 再起動 → 成功。
- プラグイン署名検証ログ: `kizuna_security.so` / `kizuna_backup_lite.so` とも Verified。
- `uninstall.sh --dry-run`: 両 unit・sudoers・ヘルパー・/etc/kizuna-eye を正しく検出。

---

## 7. 残タスク

- [ ] `scripts/verify.sh --full` は今回未実行。
- [ ] ロールバック手順（update.sh の rollback / uninstall.sh）の実機リハーサルは未実施。
- [ ] `systemd/install-services.sh` 削除に伴い、過去の作業記録（H1_PLAN 等）には旧名が残る（当時の記録のため意図的に保持）。
