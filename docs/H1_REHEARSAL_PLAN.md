# H-1 リハーサル計画（詳細版）

> 最終更新: 2026-10-05
> ステータス: 最終版（実装変更はコード完了・実機未反映）／実施は別セッション
> 目的: H-1（agent 専用ユーザー化）を本番前にリハーサルし、CR-1/CR-2 を含む手順の妥当性とロールバックを検証する。

## 実装変更の現況（2026-10-05）

| # | 変更 | コード | 実機反映 |
|---|---|---|---|
| 1 | LoadOrCreateAlertHistoryKey の 0640 化 | 完了 | 反映済み（dashboard 再起動 06:59） |
| 2 | migrate-agent-user.sh の kizuna-eye 改名＋権限追記 | 完了 | H-1 時 |
| 3 | agent unit を kizuna-eye-agent.service として導入 | 完了 | H-1 時 |
| 4 | pubkey パス統一（/etc/kizuna-eye/） | コード準備済み | H-1 前〜時（運用操作） |
| 5 | cronmon.go を直接読み取り化 | 完了 | H-1 時（プラグイン再ビルド） |

付随: watchdog/start/stop の systemd 検出に kizuna-eye-agent を追加（完了）。

---

## 0. 前提・環境

- 対象: 実機サーバー（またはテスト環境）。root で実行。
- sudo: sudo-rs 0.2.13（Ubuntu Rust 実装）。systemd 259。
- CR-2 検証は完了（2026-10-05）。**4b 失敗**（NoNewPrivileges=yes は sudo をブロック）。
- さらに **対処(a) を実機検証で確認・採用**: AmbientCapabilities=CAP_DAC_READ_SEARCH により、非 root(kizuna-eye) が /var/spool/cron/crontabs を**直接読める**（NoNewPrivileges=yes 併用可）。
- 結論: cronmon.go を「sudo ヘルパー」から「直接読み取り」へ変更する。**sudo 依存と CR-1（sudoers 追加）は不要**。
- リハーサルではこの直接読み取り経路を組み込んで検証する。
- リハーサルは「ユーザー作成 → 所有権 → systemd → 検証 → ロールバック」を1周する。

---

## 1. 事前準備

1. メンテナンス窓の確保（ダウンタイム数分）。
2. 現在の稼働状態を記録: systemctl / ps、curl /health。
3. バックアップ（手動でも取得）:
   sudo tar czf /tmp/h1-rehearsal-TS.tar.gz -C / home/user/.kizuna-eye samba/share/Kizuna-Eye/logs
4. 対処(a) 採用を反映した migrate スクリプト（改名済み、sudoers 追加なし）を準備。
5. watchdog の停止を検討: systemd user timer (kizuna-watchdog.timer) が動いていると H-1 作業中に agent を二重起動しようとする。移行前に systemctl --user stop kizuna-watchdog.timer で止める（移行後に再開）。

---

## 2. 実装変更（H-1 前に必要なもの）

1. LoadOrCreateAlertHistoryKey の作成モード 0640 化（H-1 前必須）。
2. pubkey パス統一（鍵配置 → phase4 再実行。順序厳守）。
3. migrate-agent-user.sh の kizuna-eye 改名＋権限追記（keys/鍵）。**sudoers 追加は不要**（対処(a) 採用のため）。
4. agent unit を **kizuna-eye-agent.service** として導入（NoNewPrivileges=yes は維持。AmbientCapabilities=CAP_DAC_READ_SEARCH 済み。既存 kizuna-eye.service は旧 system service で別物）。
5. **cronmon.go を sudo ヘルパーから直接読み取りへ変更**（CAP_DAC_READ_SEARCH 前提。ケーパビリティ欠如時のフォールバック警告は維持）。

---

## 3. リハーサル手順

1. CR-2 検証で作成した一時ユーザーを削除（クリーン状態に戻す）。
2. _a4_prep.sh phase1〜4 を実行（ビルド/署名/ポリシー）。
3. migrate-agent-user.sh（改名版）を実行。
4. ダッシュボード再起動（group 反映）。
5. テスト項目（H1_PLAN 8章）を順に確認。
6. ロールバックを1周。

### 実機反映の手順（1コマンド1確認）

0. watchdog 停止（H-1 中のみ）: systemctl --user stop kizuna-watchdog.timer
1. 事前確認: ps で agent/dashboard、curl http://localhost:8080/health
2. pubkey 統一（鍵配置 → phase4。順序厳守）:
   sudo install -D -m 0644 -o root -g root /home/user/.kizuna-eye/keys/plugin_signing/plugin_signing.pub /etc/kizuna-eye/plugin_signing.pub
   bash tmp/_a4_prep.sh phase4
3. migrate 実行（root）: sudo systemd/migrate-agent-user.sh user
4. dashboard 再起動（group 反映）: sudo -u user /samba/share/Kizuna-Eye/stop.sh dashboard && sudo -u user /samba/share/Kizuna-Eye/start.sh dashboard
5. watchdog 再開: systemctl --user start kizuna-watchdog.timer

重要: 実機反映（停止・再起動・pubkey 操作）の直前に、必ずユーザーの許可を取る。

---

## 4. テスト項目（具体化）

- [ ] systemctl status kizuna-eye-agent が active。
- [ ] journalctl -u kizuna-eye-agent に起動ログ。
- [ ] agent.log に両プラグインの署名検証OK・登録OK。
- [ ] version mismatch 0件。
- [ ] curl /health が agent_connected:true。
- [ ] FIM 権限不足 INFO が消える。
- [ ] 状態ファイルが /var/lib/kizuna-eye/state（0700 kizuna-eye）。
- [ ] chain.key が /var/lib/kizuna-eye/keys（0600 kizuna-eye）。
- [ ] alert_history.key が kizuna-eye から読める（0640 user:kizuna-eye、keys 2710）。
- [ ] 共有設定が 0640 user:kizuna-eye。
- [ ] 検知ログが group read で読める。
- [ ] backup プラグインが /samba/share/CD へ書ける。
- [ ] Discord 通知が届く。
- [ ] ダッシュボードから設定変更 → agent 反映（mtime 監視）。
- [ ] alert_history HMAC 署名が検証成功（不一致0件）。
- [ ] cron 監視が動作（直接読み取り経路で crontab 変更を検知）。
- [ ] agent 本体は NoNewPrivileges=yes を維持。
- [ ] kizuna-eye が CAP_DAC_READ_SEARCH で crontabs を直接読める（systemd-run 検証と同条件）。
- [ ] sudo 依存が無い（sudoers の kizuna-eye 用ルールに依存しない）。

---

## 5. ロールバック手順（1周）

1. sudo systemctl disable --now kizuna-eye-agent
2. バックアップ tar を展開（data/logs）。
3. 所有権復元: sudo chown -R user:user /home/user/.kizuna-eye/data /samba/share/Kizuna-Eye/logs
4. plugins の .so/.sig を必要に応じ 0600 へ。
5. （対処(a) 採用のため sudoers 追加は無し。ロールバックでの sudoers 復元は不要。cronmon.go の変更を元に戻す場合はコードを戻す。）
6. unit ファイルを削除し systemctl daemon-reload。
7. modules.json をバックアップから復元（絶対パス化を解除）。
8. ./start.sh で手動起動し、/health と agent.log を確認。

---

## 5.5 旧 unit ファイルの扱い

対象: systemd/kizuna-agent.service（暫定版）、systemd/kizuna-agent-a4.service（A-4 版、kizuna-eye-agent.service に置換）。

- 両者とも git 追跡下（確認済み）。履歴: 8319f94, 09dd4a9。
- 方針（推奨）: H-1 リハーサルが成功して kizuna-eye-agent.service が安定稼働するまで残置。安定確認後に退避・削除する。
  - 理由: ロールバック時に旧 unit へ戻す選択肢を残せる。
- 退避手順（安定確認後・要許可）:
  mkdir -p ~/ke-backups/systemd-old-units
  git rm systemd/kizuna-agent.service systemd/kizuna-agent-a4.service
- 注意: 既存の systemd/kizuna-eye.service（旧 system service, Type=oneshot + start.sh）は別物。触らない。
- 削除は破壊的操作のため、実施前に改めて許可を取る。

---

## 6. 記録フォーマット（セッションの目的を含む）

## YYYY-MM-DD セッション（目的）

### セッションの目的
- 何を達成するか

### 実施
- 時刻 + 操作 + コマンド

### 意思決定
- 理由つき

### 未解決の課題
- 一覧（優先度）

### 次アクション
- 具体的手順

---

## 7. 実施判断

- 本文書は最終版（実装変更1〜5 のコード完了を反映）。
- リハーサルは本番と同一手順で行い、差分を記録する。
- 実機反映（停止・再起動・pubkey 操作・旧 unit 削除）は、いずれも実施前に許可を取る。
