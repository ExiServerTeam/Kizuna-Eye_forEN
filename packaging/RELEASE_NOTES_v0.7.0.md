# Kizuna-Eye v0.7.0

低スペックサーバー向けの軽量監視ツール。今回は **インストール体験の刷新** と **APT パッケージ配布** が中心です。

## インストール（推奨: APT）

Debian / Ubuntu ではビルド不要で入れられます（依存も自動導入）。

    # 公開鍵を登録
    curl -fsSL https://exiserverteam.github.io/Kizuna-Eye_forEN/kizuna.gpg \
      | sudo gpg --dearmor -o /usr/share/keyrings/kizuna.gpg

    # リポジトリを追加
    echo "deb [signed-by=/usr/share/keyrings/kizuna.gpg] https://exiserverteam.github.io/Kizuna-Eye_forEN stable main" \
      | sudo tee /etc/apt/sources.list.d/kizuna.list

    # インストール
    sudo apt update
    sudo apt install kizuna-eye

初回アクセス: `http://<host>:8080` → `/setup` で管理者アカウントを作成。

対応: Ubuntu 20.04+ / Debian 11+（glibc 2.31 でビルド）。

## 主な変更

### Added
- APT パッケージ配布: `apt install kizuna-eye` で導入可能。GPG 署名済みリポジトリ（GitHub Pages）
- `uninstall.sh`: 導入した systemd サービス・sudoers・ヘルパー等を撤去。`--purge` で完全削除
- `install.sh` 一本化: パッケージ導入 → ビルド → プラグイン署名 → systemd 登録 → 起動まで自動
- プラグイン署名: 純正プラグインを `install.sh` / `update.sh` が Ed25519 で署名・再署名
- UI 既定言語（EN/JA）: インストール時に選択（既定 EN）。`/lang.js` で配信
- install / uninstall / migrate / start / stop のログを EN/JA 対応

### Changed
- `install.sh` を冪等化（2回目以降は再移行せず再起動のみ）
- `update.sh` / `safe_update.sh` が dashboard + agent の両方を検出して再起動
- agent 専用ユーザーを `kizuna-eye` に統一（`kizuna-eye-agent.service`）
- dashboard に `SupplementaryGroups=kizuna-eye` を追加（共有ログ閲覧）

### Fixed
- Ubuntu 26.04 以降で `. /etc/os-release` が `VERSION` を上書きしビルド失敗する問題
- root 実行時に cron / action ヘルパー導入がスキップされる問題
- `update.sh` の再署名が `/root/.kizuna-eye` を探して失敗する問題
- agent バイナリの実行権限（203/EXEC）を毎回適用するよう修正

### Security
- agent を専用ユーザーで分離（H-1）。鍵・状態は `/var/lib/kizuna-eye`（0700）
- cron 監視を `CAP_DAC_READ_SEARCH` による直接読み取りへ（sudo/sudoers 不要）
- プラグイン `.so` の Ed25519 署名検証（`require_signature` で fail-closed）

## アンインストール

    sudo apt remove kizuna-eye    # サービス撤去・データ温存
    sudo apt purge  kizuna-eye    # データ・ユーザーも削除

## 添付ファイル

- `kizuna-eye_0.7.0_amd64.deb` — Ubuntu 20.04+ / Debian 11+ 用パッケージ
