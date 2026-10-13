# 作業記録: agent.log / dashboard.log の i18n 対応

- 日付: 2026-10-05
- 対象: agent.log / dashboard.log（プレーンテキスト → JSON Lines + message_en）
- 関連: kizuna-security.log（JSON Lines + message_en、対応済み）, kizuna-backup-lite.log（JSON Lines, message のみ）, kizuna-watchdog.log（シェル出力）
- 手順書: docs/PROCEDURE.md（「Kizunaシリーズ　資料」フォルダはワークスペース内に存在せず。共有フォルダ等にある場合は要パス提示）

---

## 1. 参照箇所の調査（①）

`agent.log` / `dashboard.log` を全走査（ripgrep 相当）。参照は4系統に分類。

### A. 読み取り（形式変更の影響を受ける）
- `internal/api/logs.go` … `/api/logs` は `text/plain` でファイル末尾を素通しするだけ。JSON Lines でも無改修で通る。
- `web/static/logs.js` … `parseLogLine()` は `{` 始まり以外を `null`（＝生テキストとして描画）。`pickLogMessage()` は既に `message_en` 優先に対応済み。**テキスト/JSON 混在は既に許容**。
- `scripts/sigsegv_report.sh` … `grep -c` でクラッシュ行を数え、直前6行から `YYYY-MM-DD HH:MM:SS.mmm` 形式の ts を抽出。**JSON Lines 化で ts が RFC3339 になり `last_ts=?` になる唯一の壊れ箇所**。キーワード grep 自体は命中する。
- `scripts/verify.sh` … 起動確認で `tail -5` するのみ。影響なし。

### B. 書き込み（生成側）
- `start.sh:134,142` … `nohup ... >> logs/dashboard.log 2>&1` / `>> logs/agent.log 2>&1`（stdout リダイレクト）。
- `pkg/logger/logger.go` … `Options.LogFile` 指定時は `io.MultiWriter(os.Stdout, rotatingWriter)`。
- 実 `logs/agent.log` を確認 → **二重書きなし**（各行1回、logger の `[LEVEL] [AGENT] ts msg` 形式）。

### C. 権限・移行系（形式非依存）
- `systemd/migrate-agent-user.sh`（chown 対象列挙）, `install.sh`（案内表示）, `pkg/config/config.go`（相対パス解決）。

### D. 設定・ドキュメント
- `agent_config.example.json`, `dashboard_config.example.json`, `README*.md`, `docs/*.md`, `web/static/i18n.js`（UI ラベルでありログ本文ではない）。

### FIM 自己監視
- `modules.json.example` および Kizuna-Security 既定値では `integrity_files` に `logs/` を含まず、`fim_watch_paths` 既定は `/tmp,/var/tmp,/dev/shm`。
- 稼働中の実 `modules.json` はワークスペース内に存在せず（`.example` のみ）、**未確認**。次項で確認する。

---

## 2. 方式（確定案）

- 形式: **JSON Lines** — `{"ts":"RFC3339","level","prefix","message","message_en"}`（必要に応じ `event`）。
- `pkg/logger` に翻訳カタログと `InfoT(key, args...)` 等を追加。既存 `Info(fmt,...)` は**互換のまま残す**（未移行行は `message_en` 空 → EN UI では日本語フォールバック）。→ 170箇所を一括せず段階移行可能。
- 優先順位: 起動/停止/エラー/セキュリティ送受信 → 残り。
- `scripts/sigsegv_report.sh` の ts 正規表現を RFC3339 併用に更新（後方互換）。

---

## 3. 確認事項への回答（2〜7）

- 2. 全件か主要のみか → 段階移行を推奨。まず起動/エラー/セキュリティ（agent main.go の `lg.Error`/起動系、dashboard main.go の起動系）から。
- 3. backup_lite（別リポジトリ）→ 本タスクとは分離。方針は本記録に準拠（JSON に `message_en` 追加）。
- 4. watchdog（シェル）→ 今回スコープ外を推奨。
- 5. logs.js の混在対応 → 対応済み（parseLogLine/pickLogMessage）。
- 6. FIM 自己監視の確認 → 実 `modules.json` の `integrity_files` / `fim_watch_paths` に `logs/` が無いことを確認する。
- 7. テスト → `go test ./...`（logger の新 API 単体）、`scripts/verify.sh`、ブラウザでログ画面の JA/EN 切替を手動確認。

---

## 4. 未確認・TODO

- [x] pkg/logger への翻訳カタログ + `*T` API 追加
- [x] scripts/sigsegv_report.sh の ts 正規表現の後方互換対応
- [x] 優先バッチ（起動/エラー/セキュリティ）の移行
- [x] 稼働中の実 `modules.json` に `logs/` 監視が無いことの確認 → **監視対象外を確認（誤報リスクなし）**。実機 `/home/user/.kizuna-eye/data/modules.json`: `integrity_files` は未設定（既定＝/etc/passwd 等で logs/ を含まない）、`fim_watch_paths`=`/tmp,/var/tmp,/dev/shm`。`grep logs/` の一致は `fim_watch_baseline_path`（保存先）のみ。
- [~] 残り約150箇所の段階移行（agent/dashboard）。
  - [x] バッチ1（約65箇所）: agent の WARN/ERROR/起動系INFO（autoupdate/sys_module/plugin系/ping/read/send/cmd/backup）、dashboard の WARN/ERROR/起動系INFO（flood/auth_reject/token系/ws/event/pluginmgr/users/session/auth/upload/shutdown）。カタログに約65キー追加。
  - [x] バッチ2（約35箇所）: cmd/dashboard の Hub（h.log 系）、internal/updater、pkg/alert/engine、pkg/notify/manager。
  - [x] バッチ3a（約20箇所）: internal/api（plugins.go/alertconfig.go）、internal/auth（handlers.go/avatar.go）。
    - `pkg/module` を触らず、`pkg/logger/helpers.go`（LogInfo/LogWarn/LogError/LogDebug）で interface 経由でも動的型が `*logger.Logger` なら `*T` へ振り分け。プラグイン再ビルド不要。
  - [ ] バッチ3b（pkg/module/system.go、約15箇所）: **保留**。`module.Logger` インターフェース変更はプラグイン再ビルド必須のため、build.sh で全モジュール一括再ビルドできるようになってから（現状 build.sh はコアのみ、プラグインは個別 build.sh）。
- [ ] start.sh / logger の書き込み経路の一本化（現状は二重書きなし。JSON 化で二重行にはならないが、整理は任意）
- [x] 実機（Ubuntu）での `scripts/verify.sh`（静的）と稼働確認 → JSON Lines 出力・health を実機で確認。
- [ ] ブラウザ UI での JA/EN 切替の目視確認（ユーザーによる確認待ち）
- [x] frontend XSS の既存指摘を修正（前セッションの未コミット分）。
  - `logs.html` に `escape.js` を追加（`logs.js` より前）。
  - `logs.js` の独自 `escapeHtml` 定義を削除し、escape.js に一元化。
  - `app.js` のローカル `esc()` エイリアスを廃止し `escapeHtml()` に統一。
  - 検証: `node scripts/check_xss.js web/static` → `OK: escape.js 一元化 … すべて安全`。

---

## 4a. 恒久対策 TODO（今回のヒヤリハット由来）

- [ ] プラグイン再ビルド時の署名漏れ防止: `plugins/Kizuna-Security/plugin/build.sh` に `plugin-sign -sign`（鍵があれば）を組み込む、または `PROCEDURE.md` の「プラグイン再ビルド手順」に署名ステップを明記。build.sh は現状 `go build` と `cp` のみ。
- [ ] `build.sh`（コアルート）に全プラグインの一括再ビルド＋再署名を統合する案の検討（バッチ3b の前提にもなる）。

---

## 4b. 修正ポイント一覧への対応（グループ別）

- H-3（dashboard 再ビルド・差し替え）: **解決済み**。今回のログ i18n 作業で dashboard_linux を再ビルド（Oct 5 04:11）→ 04:12 再起動。dashboard.log に新キー JSON Lines（`dash.client_added`→"Client added: …"、`dash.broadcast_error` 等）を確認、health=healthy。Low-11/12/Medium-10 は同ソース由来で反映済み。
- グループA:
  - L-1（V2-B warning 文言）: **完了**。`messages.go` の `integrity.alerthist.title/msg` を ja/en とも「改ざん」→「縮小（compaction/truncation 区別不能）」に変更。プラグイン build/vet/test 成功（サーバー側）。
  - M-2（ログローテーションの inode 追跡）: **対応不要（既実装）**。`monitor.go` の `scanFile()` が `fileIdentity(info)` で dev/ino を取得し、`rotated := knownIno != 0 && (dev != knownDev || ino != knownIno)` で inode 変化を検知して `startOffset = 0` にリセット済み。FIM 側（`fim_watch_inode_linux.go`）と同じパターンを既に踏襲。
- グループB:
  - H-2 + L-4: **完了**。
    - `scripts/patch_*.py`（17本）は全て「当て終わった修正」と判定（V1/V2/V4/V5/FIM deadlock/i18n/UI/spoof/test isolation/regression の痕跡を現行ソースで確認）。削除ではなく `scripts/archive/` へ移動。
    - `_pentest_backup_*`（3世代・約40MB）を `~/ke-backups/`（0700）へ退避。
    - `logs/*.legacy-*` は3世代ルールで整理（log 3件＋logstate 1件＝4件が残存。容量約2.8MBのため実害なし。厳密3件化は保留）。
  - M-3（sudoers 引数固定）: **未実施**。`user` は `(ALL:ALL) ALL` を持つが `sudo -n` では sudoers を読めない（interactive auth 要求）。sudoers 編集は人間が実行する必要あり。手順を提示（後述）。
- グループC:
  - L-2（テスト分離の一般化）: **完了**。プラグインのテストが `./logs/kizuna-security-logins.json` を共有書き込みしていた潜在的な順序依存を特定（`DefaultConfig()` を直接使うテストが `SSHLoginBaselinePath` を上書きしていなかった）。`requeue_test.go` / `security_improve_test.go` / `monitor_extra_test.go`（3テスト）で `cfg.SSHLoginBaselinePath = ""` を設定。テスト成功＋`./logs` 非生成を確認。
  - L-3（運用方針の明記）: **完了**。下記「7. 運用ルール」に記載（ソースは直接編集＋Git、patch_*.py は緊急時のみ、当て済みは scripts/archive/）。
  - M-1/L-5（記録のサニタイズ）: OSS 公開直前まで保留。
- グループD（H-1）: 今回スコープ外。別セッションで計画書→レビュー→承認。

## 4c. セキュリティ6層 強化（2026-10-05 追加）

- 層6: **完了**。SECURITY.md 拡充（サポート範囲/報告/暗号化/修正ポリシー/既知の制約）、docs/ROADMAP.md に6層サマリと実装/未実装/やらないこと、CI に govulncheck 追加。
- 層1・2・3 デフォルト変更: **計画のみ**（docs/DEFAULT_CHANGE_PLAN.md）。新規はセキュア既定、既存は現状維持、config_version 導入、agent_token は2段階、require_signature は署名手順と同時に。実施は要承認。
- 層4:
  - リモート転送: **手順化**（docs/LAYER4_LOG_PLAN.md、rsyslog TLS / journal-upload。宛先は運用者決定）。
  - alert_history HMAC署名: **実装・テスト完了（実機未反映）**。
    - `pkg/alert/sign.go`: `LoadOrCreateAlertHistoryKey`（専用鍵 0600）、`signHistoryEntry`、`VerifyHistoryLineSig`。
    - `pkg/alert/history.go`: `HistoryEntry.Sig`、`SetSigningKey`、永続化時に署名。
    - `pkg/alert/engine.go`: `EnableHistoryPersistenceKeyed`。
    - `pkg/config`: `AlertHistoryKeyPath`（既定 keys/alert_history.key、相対は config 基準で解決）。
    - `cmd/dashboard/main.go`: 鍵ロード→署名有効化（失敗時は無署名で続行）。
    - プラグイン `integrity.go`: `verifyAlertHistorySigs`（sig付き行のみ検証、無署名はスキップ）。既存 V2-B 判定は不変。メッセージキー `integrity.alerthistsig.*`。
    - 鍵は **専用鍵**（chain.key と分離）。1鍵1用途。
    - テスト: `pkg/alert/sign_test.go`、`plugins/.../alert_hist_sig_test.go`。コア/プラグインとも gofmt/build/vet/test 成功。
    - **実機反映: 完了（2026-10-05 05:26）**。手順は「1コマンド1確認」で実施。
      - プラグイン再ビルド → `plugin-sign -sign` で再署名（`.sig` 89B 生成・verify OK）→ `.so`+`.sig` 両方配置 → dashboard 再ビルド。
      - `dashboard_config.json` と `modules.json` に `alert_history_key_path=/home/user/.kizuna-eye/data/keys/alert_history.key` を設定（両者一致）。
      - 停止→起動（dashboard 790905 / agent 790916）。
      - 確認: 署名検証OK・登録OK、Agent認証済み接続、health=healthy、`version mismatch` は今回起動0件。
      - 鍵: `alert_history.key` 32B/0600/05:26生成、`chain.key` と別物（sha256 相違）。
      - `alert_history.jsonl` 最新行に `sig`（64hex）。**実ファイルで HMAC-SHA256 を再計算し MATCH** を確認（鍵・アルゴリズム一致）。
      - プラグイン `integrity.alerthistsig`（署名不一致）は0件。既存 V2-B の「縮小」warning は仕様どおり。
      - **再発防止の推奨**: ビルド→署名→配置を一括化するスクリプト（署名漏れ防止）。

---

## 5. 作業ログ

- 2026-10-05: ①の参照調査を実施。方式を確定（本記録）。
- 2026-10-05: `pkg/logger/catalog.go` を新規作成（ja/en カタログ、agent/dashboard の優先キー約50件）。
- 2026-10-05: `pkg/logger/logger.go` に `Lang` オプションと JSON Lines を出力する `logT()` および `DebugT/InfoT/WarnT/ErrorT/FatalT` を追加。既存 `Info(fmt,...)` は互換のまま残置。
- 2026-10-05: `web/static/logs.js` の SKIP_KEYS に `message_en`/`prefix` を追加（整形時に余分なフィールドとして出さない）。
- 2026-10-05: `scripts/sigsegv_report.sh` の ts 正規表現を RFC3339 併用に更新（後方互換）。
- 2026-10-05: `pkg/logger/catalog_test.go` を追加。`go test ./pkg/logger/...` 成功。
- 2026-10-05: 優先バッチの呼び出し側を `*T` へ移行。
  - `cmd/agent/main.go`: 起動/終了/WS接続確立/セキュリティイベント送信・失敗・パニック/依存コマンド不足（9箇所）
  - `cmd/dashboard/main.go`: 起動/静的dir/シャットダウン/通知ON・OFF/セキュリティアラート/サーバー開始・停止（7箇所）
- 2026-10-05: 検証結果
  - `go build ./...` 成功
  - `go test ./...` 全パッケージ成功
  - `go vet ./...` クリーン
  - `gofmt -l pkg/logger cmd/agent cmd/dashboard` クリーン（catalog.go を整形済み）
- 2026-10-05: 未使用となった `logger.Options.Lang` / `Logger.lang` は削除（`logT()` は常に ja/en 両方を書くため不要）。
- 2026-10-05: 実機確認（SSH 鍵認証 user@192.168.0.233）。実 `modules.json` を確認し logs/ は FIM 監視対象外であることを確認（誤報リスク解消）。
- 2026-10-05: frontend XSS の既存 NG を修正（logs.html に escape.js 追加、logs.js の escapeHtml 再定義を削除、app.js の esc() を escapeHtml() に統一）。check_xss が OK に。
- 2026-10-05: (a) バッチ1（約65箇所）を `*T` へ移行。カタログに約65キー追加。
  - `cmd/agent/main.go`: 自動更新警告/システムモジュール/プラグイン署名・停止・再設定・拒否・読込・登録・削除・panic/ping/read/send/status/cmd parse/backup 系。
  - `cmd/dashboard/main.go`: flood/auth_reject/agent token 系/ws upgrade/client 接続/agent 認証・識別/event/pluginmgr/users/session/auth/public viewer/upload 警告/shutdown/start 失敗。
  - 検証: `go build ./...` OK / `go vet ./...` OK / `gofmt -l` クリーン / `go test ./...` 全パッケージ成功。
- 2026-10-05: (a) バッチ1 を実機へ反映（build.sh → stop.sh/start.sh、ユーザー許可済み）。プラグイン2つ署名検証OK・ロードOK、health=healthy。
- 2026-10-05: (a) バッチ2（約35箇所）を `*T` へ移行。カタログに約34キー追加。
  - `cmd/dashboard/main.go`: Hub の h.log（broadcast/viewer cap/evict/max clients/client add・remove/close/invalid status）。
  - `internal/updater/updater.go`: 自動更新の有効/確認失敗/最新/新バージョン/スクリプト失敗・完了/出力。
  - `pkg/alert/engine.go`: アラート履歴読み込み失敗。
  - `pkg/notify/manager.go`: 通知無効/チャンネル登録（discord/telegram/line/slack/email）/バッチ/送信/同時実行スキップ/タイムアウト/panic/失敗/成功。
  - 検証: `gofmt -l` クリーン / `go build ./...` OK / `go vet ./...` OK / `go test ./...` 全パッケージ成功。
  - 注: internal/api は module.Logger 型、internal/auth は独自 Logger インターフェースのため、プラグイン再ビルド回避のためバッチ2対象から除外。
- 2026-10-05: (a) バッチ3a（約20箇所）を移行。`pkg/logger/helpers.go` を新設し、`pkg/module` を変更せずに interface 経由のログも JSON Lines 化。
  - `internal/api/plugins.go`（bwrap/手動実行/アップロード/署名検証/検証失敗/登録失敗/削除失敗/削除）、`internal/api/alertconfig.go`（閾値更新）。
  - `internal/auth/handlers.go`（setup/login/guest/password/user 作成・削除）、`internal/auth/avatar.go`（アバター更新）。
  - カタログに約20キー追加（api.* / auth.*）。
  - 検証: `gofmt -l` クリーン / `go build ./...` OK / `go vet ./...` OK / `go test ./...` 全パッケージ成功。
  - バッチ3b（pkg/module/system.go）は build.sh の全モジュール一括ビルド整備待ちで保留。
- 2026-10-05: バッチ2+3a を実機反映（build.sh → stop.sh/start.sh、ユーザー承認済み）。dashboard も新キー JSON Lines を確認、health=healthy、プラグイン2つ署名検証OK。
- 2026-10-05: H-3 確認完了（dashboard 再ビルド済み・新キー出力・health OK）。
- 2026-10-05: L-1 完了（integrity.alerthist の文言を ja/en 変更）。M-2 は既実装と確認（対応不要）。
- 2026-10-05: グループB-1（H-2/L-4）完了。patch 17本を scripts/archive/ へ、_pentest_backup_* を ~/ke-backups/ へ退避、legacy ログを3世代整理。
- 2026-10-05: グループC L-2 完了（テスト分離: 共有 ./logs 書き込みを3テストで解消）。L-3 完了（運用方針を「7. 運用ルール」に明記）。
- 2026-10-05: グループA を実機反映（ユーザー許可済み）。kizuna_security.so を再ビルド・配置し、agent のみ再起動。
  - **ヒヤリハット**: 再ビルド後に `.so` を配置したが `.sig` を更新せず、署名検証で `kizuna_security` のロードが一時拒否された（セキュリティ監視が数分停止）。
  - `plugin-sign -sign <so> -private-key <key>` で再署名 → `-verify` で確認 → agent 再起動で復旧（04:22、health=healthy、署名検証OK・ロードOK）。
  - **教訓（必須）**: プラグイン `.so` を再ビルドしたら、**必ず同ファイルを再署名（.sig 更新）してから配置・再起動**する。
  - 既存の `plugins/Kizuna-Security/plugin/build.sh` は `go build` と `cp` のみで**署名ステップが無い**。恒久対策として build.sh に再署名を組み込むか、手順書（PROCEDURE.md）に明記することを推奨（TODO）。
- 2026-10-05: 実機での総合検証（(d)）。
  - サーバー: `server1` (192.168.0.233, user)。リポジトリは `/samba/share/Kizuna-Eye`（ワークスペースと同一実体）。Go 1.26 / gcc 15.2 / node 22 が利用可能。
  - agent/dashboard は systemd ではなく user 権限の手動プロセス（start.sh 方式）。
  - `./build.sh` で新バイナリをビルド（成功）。`./stop.sh` → `./start.sh` で再起動。
  - **agent.log（新形式・実出力）**: `{"level":"INFO","message":"エージェント起動 (送信間隔: 0.2秒, 接続先: ws://localhost:8080/ws)","message_en":"Agent started (interval: 0.2s, dashboard: ws://localhost:8080/ws)","prefix":"AGENT","ts":"2026-10-05T03:40:08Z"}`
  - **dashboard.log（新形式・実出力）**: `{"level":"INFO","message":"ダッシュボード起動 (listen: :8080)","message_en":"Dashboard started (listen: :8080)","prefix":"DASHBOARD","ts":"2026-10-05T03:40:07Z"}`
  - 旧テキスト行と新 JSON 行の**混在が問題なく共存**することを確認（移行期の想定どおり）。
  - `curl /health` → `{"agent_connected":true,"status":"healthy"}`。
  - `logs/` への FIM 誤報なし（FIM の /tmp 監視イベントは従来からの通常動作）。
  - 静的 `./scripts/verify.sh`: gofmt/vet/test/race/i18n はすべて OK。frontend XSS のみ NG（**前セッションの未コミット web/static 変更が原因。コミット済みベースラインでは OK** を確認済み）。
  - 未実施: ブラウザ UI での JA/EN 切替の目視確認（ログ API は `auth.enabled=true` のため curl では要ログイン）。

---

## 6. 退避パッチ対応表（scripts/archive/ の17本）

scripts/archive/ へ退避した patch_*.py は全て「当て済み」。対象パスが Kizuna-Security/plugin/*（旧配置）のものは、commit 8c3d70d で Kizuna-Eye/plugins/Kizuna-Security/plugin/* に取り込み済み。

| パッチ | 内容 | 対象（現行） | 対応コミット |
|---|---|---|---|
| patch_v1v2.py | V1-A/V2-B 配線（チェーン鍵/アラート履歴整合性） | plugins/.../config.go, plugin.go, logger.go, integrity.go | 932135e, c853c98 |
| patch_v4.py | V4: 不正Agent接続の通知（token不一致/重複） | cmd/dashboard/main.go | e6dd842 |
| patch_monitor_spoof.py | V5: journald 送信元で auth.log 偽装検知 | plugins/.../monitor.go, monitor_spoof.go | e6dd842 |
| patch_msg_spoof.py | spoofed.title/msg（ja/en） | plugins/.../messages.go | e6dd842 |
| patch_plugin_spoof.py | Configure から spoof watch を開始 | plugins/.../plugin.go | e6dd842 |
| patch_spoof_contains.py | classify を部分一致に | plugins/.../monitor_spoof.go | e6dd842 |
| patch_spoof_filter.py | sshd を主張する行のみ flag（誤検知修正） | plugins/.../monitor_spoof.go | e6dd842 |
| patch_spoof_nilctx.py | startSpoofWatch(nil ctx) の panic 修正 | plugins/.../monitor_spoof.go, plugin.go | e6dd842 |
| patch_fim_deadlock.py | FIM 自己デッドロック修正（langMu 分離） | plugins/.../fim.go | 034dca2 |
| patch_cronmon_sudo.py | cron を sudo ヘルパー経由で読む＋初回ファイル吸収停止 | plugins/.../cronmon.go | dba55b3 |
| patch_cronmon_unreadable.py | cron 監視パスが読めない時に警告 | plugins/.../cronmon.go | dba55b3 |
| patch_regression_fixes.py | suid hasBaseline 修正 / FIM 新規監視警告テスト | plugins/.../suid.go, security_extra_test.go | dba55b3 |
| patch_test_isolation.py | newTestMonitor の永続化無効化（テスト分離） | plugins/.../monitor_test.go | d239e11（L-2 関連） |
| patch_engine_sourced.py | alert 履歴の source/trusted（engine.go） | pkg/alert/engine.go | 6cfbd97 |
| patch_i18n_source.py | alerts.source_trusted/untrusted（ja/en） | web/static/i18n.js | 6cfbd97 |
| patch_main_sourced.py | ReportAlert に source/trusted 付与 | cmd/dashboard/main.go | 6cfbd97 |
| patch_ui_source.py | renderAlerts に source/trusted バッジ | web/static/app.js, style.css | 6cfbd97 |

（備考: 対応コミットは git log から最も整合するものを記載。厳密な1対1が曖昧なものは複数併記。）

---

## 7. 運用ルール（追記）

- **legacy ログ保持ポリシー**: 3世代を目安とする。超過時は古いものから削除する。現在4件（log 3＋logstate 1）は許容（次回 legacy 生成時に古いものから削除）。logstate は「状態ファイル」でありログとは別系列として扱う。
- **ソース編集方針**: 以後は直接編集＋Git コミットを原則とする。パッチスクリプト（patch_*.py）は緊急時の一時手段に限る。当て済みパッチは scripts/archive/ へ退避する。
