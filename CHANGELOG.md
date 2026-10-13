# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- エージェントから受信したステータスを範囲検証（`CPUUsage` / `MemPercent` / `DiskPercent` が 0〜100 の範囲外なら破棄）。不正値がメトリクス履歴・`/api/metrics` に流れ込むのを防止
- プラグインアップロードをトランザクション化。検査失敗・`meta.json` 書き込み失敗・`modules.json` 登録失敗のいずれでも孤児 `.so` / `meta.json` を残さない
- プラグイン削除で `.so` の削除に失敗した場合は 500 を返す（実行可能な孤児 `.so` を残したまま「削除成功」を返さない）
- `plugin-inspect` の反射呼び出しにシグネチャ検証と recover を追加（悪意ある `.so` による panic を防止）
- 設定エディタの秘密復元を位置ではなく `type` で対応付け（通知チャンネルの並べ替え・削除で別チャンネルの秘密を誤割り当てしない）

### Fixed
- 手動バックアップ実行の二重起動ガードを `request_id` 照合に変更（定期実行の結果で誤って解除されない）
- 無効化したプラグインを停止する `ModuleManager.Unregister` を追加（Agent 再起動まで動き続ける問題を修正）
- エージェント切断時のセキュリティイベント消失を修正（送信失敗時は未送信分を再キュー）
- 通知プール飽和時に critical 通知まで失われる問題を修正（critical は最大5秒スロット解放を待つ）
- ログのローテーション失敗時に以降のログが失われる問題を修正（ハンドルを再オープン）
- ログ読み取り（`readLastLines`）のメモリ使用量に上限（4MiB）を設け、改行の少ない巨大ファイルによる枯渇を防止
- `Hub` のエージェントコールバック（status / disconnect / event）を hub ロックで保護（データ競合を解消）
- アラート設定の同時 PUT によるデータ競合を解消（mutex で直列化）
- `handleDashboardCommand` / `sendBackupResult` の nil ロガー参照を修正
- 通知チャンネル `type` の大文字小文字・空白を正規化（`"Discord"` 等を受理）
- 設定ファイル（agent / dashboard / modules / users）を読み込み時に 0600 へ締め付け

### Changed
- `.gitattributes` を追加して改行コードを LF に正規化（gofmt の安定化）
- README（英/日）を実装に合わせて全面的に整理（壊れていたコードフェンスと古い設定例を修正）
- `scripts/verify.sh` は gcc 未検出時に `-race` を「失敗」ではなく「スキップ」に変更

### Added
- ログイン / 初期セットアップ / ユーザー管理画面にダーク・ライトテーマ切替を追加（ダッシュボードと同じ `kizuna-theme` を共有、OS 設定にも追従）
- 認証・ユーザー管理（任意、`auth.enabled` で有効化）
  - 初回アクセスで `/setup.html` に誘導し、最初のユーザーを管理者として作成
  - ロール（admin / operator / viewer）に応じたページ表示制御と API 認可
  - パスワードは bcrypt でハッシュ化して `users.json` に保存
  - セッション Cookie（HttpOnly / SameSite=Lax / Secure 対応、ログイン時の再生成）
  - 最後の管理者の削除・降格を防止
  - ログイン試行の IP 単位レート制限（10回/5分でロックアウト）
- Agent の WebSocket トークン認証（`auth.agent_token` と `agent_config.json` の `token`）
- ヘッダー右上にユーザー名とログアウト、管理者には「ユーザー」リンクを表示

### Fixed
- 認証有効時に `style.css` と `auth-theme.js` が公開パスに無く、ログイン / セットアップ画面で読めずテーマが正しく表示されない問題（公開パスに追加）
- 設定エディタ（GUI）で通知チャンネルを開くと、Discord 以外のチャンネル（Slack / Telegram / LINE / Email）の `type` を `discord` に上書きしてしまい、保存すると他チャンネル設定が失われる問題（GUI が編集できるのは Discord のみとし、他種別は変更せず保持するよう修正）

### Fixed
- Email 通知の `smtp.SendMail` が context/タイムアウトを尊重せず、SMTP サーバー無応答時に goroutine がハングして通知セマフォを枯渇させる問題（`net.Dialer.DialContext` + `smtp.NewClient` に置換し、STARTTLS を維持しつつ deadline を適用）

### Security
- WebSocket の読み取りメッセージサイズを制限（Dashboard 8MiB / Agent 1MiB。巨大フレームによるメモリ枯渇を防止）
- プラグイン Web UI（`/plugins/{name}/`）にセキュリティヘッダ（`X-Content-Type-Options` / `X-Frame-Options` / `Referrer-Policy`）を追加（専用ハンドラのため静的配信のヘッダが適用されていなかった）
- 設定・ログ・プラグインの各ディレクトリを 0700（所有者のみ）で作成するよう統一（秘密情報・実行ファイル・ログを含むため）
- モジュール一覧の描画で、JSONインポートやプラグイン由来の値（`version_label` / `interval` / `last_run` / `size` / `status` / `monitor_state` / プラグイン設定の `min` / `max` / ディスク `health`）が未エスケープだった箇所を `escapeHtml` / `escapeAttr` で修正（読み込んだ JSON や悪意あるプラグインメタデータ経由の XSS を防止）
- Agent 接続のヒューリスティック識別を `role=agent` 宣言時のみに制限（ブラウザが status 風 JSON を送って Agent 接続を奪える問題を修正）。Agent は常に `role=agent` を送信
- リクエストボディのサイズ制限を追加（JSON API 1MiB / プラグインアップロード 64MiB。巨大ボディによるメモリ・ディスク枯渇を防止）
- アラート履歴ファイルの無限肥大を防止（4MiB 超過で最新エントリのみに圧縮、起動時 Load 後も圧縮）
- ログ SSE がログローテーション（ファイル縮小）を検知して先頭から再開するよう修正（従来は以降のログが永遠に配信されなかった）
- WebSocket の `CheckOrigin` が全許可だった問題を修正（クロスサイト WebSocket ハイジャック対策。同一ホストのみ許可、非ブラウザの Agent は Origin なしで許可）
- ログイン試行のレート制限が `X-Forwarded-For` を無条件に信頼しており、ヘッダ偽装でバイパス可能だった問題を修正（ループバック接続からのみ XFF を信頼）
- Slack 通知の mrkdwn インジェクション（`<!channel>` 等でメンション爆撃・偽リンク）を防止。Discord は `allowed_mentions.parse=[]` でメンションを無効化
- フロントのプロトタイプ汚染ガード（プラグイン由来キーの `__proto__` / `constructor` / `prototype` を拒否）
- プラグインの `meta.json` を 0600 で保存（アップロード元 IP を含むため）
- SSE ログ配信の初期ダンプを末尾 200 行に制限（ファイル全体送信による過負荷を防止）
- Hub の `GetLastStatus` / `SetLastStatus` をディープコピーに変更（スライス共有の防止）
- 静的配信にセキュリティヘッダを追加: `Content-Security-Policy`（インラインスクリプト禁止）/ `X-Content-Type-Options: nosniff` / `X-Frame-Options: DENY` / `Referrer-Policy: same-origin`（クリックジャッキング・MIMEスニッフィング対策）
- 設定ファイル（`dashboard_config.json` / `agent_config.json` / `modules.json`）を新規作成する際の既定権限を 0600 に変更（秘密情報を含むため）
- プラグイン設定フォームで、プラグイン由来の値（`f.min` / `f.max` / キーから生成する `id`）をエスケープ。悪意あるプラグインのメタデータによる XSS を防止
- プラグイン Web UI（`/plugins/{name}/`）を operator 以上に制限（従来はログイン済みなら viewer でも閲覧可能だった）
- `GET /api/config*` が秘密情報（`agent_token` / `webhook_url` / `bot_token` / `smtp_password` / LINE `token` 等）を平文で返していた問題を修正。マスク（`***`）を返し、マスクのまま保存しても既存の秘密を保持する
- ログファイル・アラート履歴ファイルを 0600（所有者のみ）で作成し、既存ファイルの権限も起動時に 0600 へ締めるよう修正（ユーザー名・IP 等の漏洩防止）
- アラート履歴ディレクトリを 0700 に変更

### Fixed
- **重大**: `Hub.Remove` がロック保持中に `onAgentDisconnect` を呼んでおり、デッドロックの危険があった問題（ロック解放後に呼ぶよう修正）
- **重大**: `updateStatus` がネットワークの前回値（`prevNetUp` 等）をロック外で読み書きし、データ競合していた問題（ロック下に移動）
- **セキュリティ**: `users.json` が破損していると `NeedsSetup()` が true になり、`/setup` から誰でも管理者を作成できてしまう問題（破損時はセットアップを拒否し、手動修正を要求するよう修正）
- **重大**: 認証有効時、Agent の WebSocket 接続が認証ミドルウェアに遮断され接続できなかった問題（`/ws?role=agent` + 正トークンはミドルウェアを通過し、`/ws` ハンドラでトークン検証するよう修正）
- 認証無効時に `/api/auth/status` が `needs_setup: true` を返し、`/login.html` から `/setup.html` へ誤リダイレクトされる問題（`auth_enabled` を返し、フロントで判定）
- プラグインアップロード制限（`plugins_upload_enabled`）が機能していなかった問題（未設定/`false` 時は `POST /api/plugins/upload` を 403 で拒否）
- `plugins_dir` 設定が無視され、常に実行ファイル隣接の `plugins/` を参照していた問題（`ResolvePluginsDir` / `EnsurePluginsDir` を使用）
- gorilla/websocket の同時書き込み禁止違反（Agent: `wsWriter`、Dashboard: `writeMu` で書き込みを直列化）
- `Start()` 後に登録されたモジュール（ホットリロードされたプラグイン）が実行されなかった問題（`Register` で実行ループを起動）
- CPU温度センサーが取得できない場合にアラートの復旧状態が固まり、復旧通知が出なくなる問題
- ログ SSE の読み取り位置が、末尾に改行がない最終行で 1 文字ずれる問題
- ダッシュボードのバックアップ表示がステータスを三重に上書きしていた問題（表示処理を統合、未定義だった CSS クラス `error` を `failed` に統一）
- i18n キー整合性チェッカーが `data-i18n-*` と `t("key")` を検出できていなかった問題

### Added
- `dashboard_config.json` の `log_level` を反映（`debug` / `info` / `warn` / `error`、未設定時は従来どおり `debug`）
- 全ページのヘッダーに接続ステータスバッジを追加（状態に応じて緑/黄/赤の色ドットで表示）

### Removed
- 未使用の `parseSize` 関数（`cmd/agent`）
- 未使用パッケージ `internal/transport`・`internal/collector`・`internal/render`（本番コードから参照なし。Agent/Dashboard は `cmd/*/main.go` に直接実装）
- `notify.EmailNotifier` の未使用フィールド `useTLS`（`smtp.SendMail` が STARTTLS を自動交渉するため不要）

## [0.6.1] - 2026-09-26

### Added
- 設定エディタに「ひな形を挿入」機能（agent/dashboard/modules のテンプレート）
- Prometheus メトリクスに `kizuna_up`（死活監視用）を追加
- Phase 10-4: アラート履歴（`pkg/alert.History` リングバッファ + `GET /api/alerts` + ダッシュボード表示）
- バージョン情報 API（`GET /api/version`）
- 言語切替ボタンを全画面のヘッダーに配線（`i18n.js` の読み込み漏れも修正）
- プラグイン専用 Web UI 配信（`GET /plugins/{name}/`、`plugins/<name>.web/` 配下、パストラバーサル対策済み）
- モジュール管理画面に Web UI を持つプラグインの「開く」ボタンを追加
- Phase 10-8: ログ専用ページ（`logs.html`、Agent/Dashboard切替・行数指定・自動更新・全画面ナビに「ログ」追加）
- Phase 13: Slack 通知チャネル（Incoming Webhook）
- Phase 13: Email（SMTP）通知チャネル
- ナビゲーションの i18n 対応（`data-i18n` 付与）
- 全画面の英語対応（ダッシュボード・モジュール管理・設定エディタ・ログの動的文言を `t()` 化、言語切替時に再描画）
- スマホ最適化：`viewport-fit=cover`、セーフエリア対応、ヘッダー上部固定（sticky）、ナビの横スクロール＋スナップ、モーダルのシート風表示、入力欄16px固定（iOS自動ズーム防止）、タップ最小サイズ確保、`prefers-reduced-motion` 対応
- プラグインステータスに登録プラグイン一覧を追加（`/api/modules` から取得、有効/無効バッジ付き）

### Fixed（追加）
- プラグインステータスの「モジュール数」が常に 0 のまま更新されなかった問題
- `/api/alerts` にフィルタ（`?limit=N&level=critical`）を追加

### Fixed（追加レビュー分）
- `metrics.go`: ディスク温度メトリクスの `# HELP`/`# TYPE` が複数ディスクで重複出力される問題
- `web/static/logs.js`: テーマ切替ボタンが未配線だった問題
- `web/static/app.js`: アラート履歴の折りたたみ矢印が回転しなかった問題
- `web/static/modules.css`: `.btn-open`（プラグインUIを開く）のスタイル未定義
- `build.sh`: バージョン/ビルド日時を ldflags で埋め込むよう修正
- `web/static/app.js`: i18n の `t()` を局所変数 `const t = Number(...)` がシャドウイングしていた問題（`tempNum`/`tempVal` に改名）
- i18n キー整合性チェッカー `scripts/check_i18n.js` を追加（未定義キー・ja/en 欠落を検出）
- Prometheus メトリクス拡充（CPUコア別・ロードアベレージ・ディスク別・プロセス数・バックアップ・鮮度メトリクス）
- `ConfigHandler.validateConfig` を設定更新時に適用
- Dashboard main.go にメトリクス/ログ/設定 API の配線を追加

### Changed
- `StatusHub` を型付き（`*status.SystemStatus`）に変更し JSON 往復を排除
- `metrics.go` を `strings.Builder` + ヘルパーで再実装
- 手書き JSON 応答を共通ヘルパー（`writeJSON` / `writeDashboardJSON`）に統一
- バージョン表記を v0.6.1 に統一（web 全体）

### Fixed
- `/api/metrics` の CPU メトリクスが `cpu`（誤）→ `cpu_usage`（正）で出力されず欠落していたバグ
- ログ SSE のワイルドカード CORS を削除
- `joinStrings` を `strings.Join` に置換、CPU温度メッセージの値重複を解消
- `collector/runner.go`: `panicError.Error()` が panic 値の型アサーションで再 panic するバグ（`fmt.Sprintf` に変更）
- `collector/runner.go` / `scheduler.go`: `Interval <= 0` で `time.NewTicker` が panic する問題にガード追加
- `transport/http.go`: Content-Type の完全一致チェックで `application/json; charset=utf-8` を誤って 415 拒否する問題
- `transport/http.go`: `StartServer` で `/api/status`・`/health` を二重登録し `ServeMux` が panic する問題
- `transport/ws.go`: `Close()` がロック保持中に `StopServer()` を呼びデッドロックする問題
- `render/layout.go`: `Render()` の RWMutex 二重 RUnlock による panic
- `notify/telegram.go`: `parse_mode: Markdown` が本文の特殊文字で 400 になる問題（HTML + エスケープに変更）
- `transport/file.go`: `Receive()` が RLock 保持中に Lock を取りデッドロックする問題
- `status/reader.go`: `Clone()` が「ディープコピー」と明記されているのに浅いコピーだった問題（スライス/ポインタを複製）
- `cmd/dashboard/main.go`: `Hub.SetLastStatus(nil)` が nil 逆参照で panic する問題（テストで検出）
- `cmd/dashboard/main.go`: `NewLogHandler` の agent/dashboard ログ引数が逆になっていた問題
- `render/layout.go` / `partials.go`: テンプレートのグロブ不一致で `template.Must` が起動時に panic する問題（非 panic 化 + パス修正）
- `internal/api/logs.go`: `readLastLines` がファイル全体をメモリ展開していた問題（末尾ブロック読みに変更、行数上限10000）
- `internal/api/plugins.go`: プラグイン登録時の `plugin_path` を文字列連結で JSON 化していた問題（`json.Marshal` に変更）
- `web/static/config-editor.js`: `modules` 選択時のタイトルが `modules_config.json` と誤表示される問題

### Changed（追記）
- 全 Go ファイルを `gofmt` で統一整形
- `pkg/module/base.go`: `ModuleManager.Start` でモジュール一覧をロック下でスナップショットし、`Register` とのデータ競合（concurrent map iteration and map write）を防止

### 運用上の注意
- `pkg/status` を変更した場合は、**Agent 本体と全プラグイン（`.so`）を同じソースから同時に再ビルド**すること。Go プラグインは共有パッケージのハッシュ完全一致が必須で、片方だけ再ビルドすると `plugin was built with a different version of package ...` でロードに失敗する。

## [0.6.0] - 2026-09-22

### Added
- Phase 8-5: Dashboard からの手動バックアップ実行
- Phase 8-4: cron式スケジューラ（`github.com/robfig/cron/v3`）
- Phase 8-6: 完全自動プラグインアップロード機能
- `plugin-inspect` バイナリによる別プロセス検証
- `ConfigField` / `ConfigProvider` インターフェース
- `DisplayNameProvider` インターフェース
- 動的プラグインフォーム生成（`modules.js`）

### Changed
- `internal/api/plugin.go` → `internal/api/plugins.go` にリネーム
- プラグインの `.so` を別プロセスで検証する方式に変更
- `modules.json` の `plugin_path` を `/opt/kizuna-eye/bin/plugins/` に統一
- `AgentHub` インターフェースを追加（Dashboard から Agent へのコマンド送信）

### Fixed
- Agent の30分ごとの切断バグ（`ReadDeadline` リセット漏れ）
- `SetWriteDeadline` を5秒から30秒に延長
- `omitempty` による `processes` フィールドの消失

## [0.5.0] - 2026-09-17

### Added
- Phase 8-1: LITE版プラグイン（Go完全書き直し）
- JSON Lines形式のログ出力
- `rotationLocal` による世代管理
- `SHA256File` によるハッシュ計算

## [0.4.0] - 2026-09-15

### Added
- Phase 7: SNS通知（Discord / Telegram / LINE）
- Agent死活監視（`agent_timeout_sec`）
- アラート判定エンジン（`pkg/alert/engine.go`）

## [0.3.0] - 2026-09-10

### Added
- Phase 5: CPUコアグリッド表示
- 空き容量バッジ
- アラートPulseアニメーション
- スパイク制御（`holdMs` / `cooldownMs`）

## [0.2.0] - 2026-09-05

### Added
- Phase 3: CPU型番・温度・Load Average・ネットワークIO収集
- Phase 4: ディスクS.M.A.R.T・ネットワーク速度

## [0.1.0] - 2026-09-01

### Added
- Phase 1: ライトモード視認性改善
- Phase 2: 単位統一・空状態CTA・トグルUI統一
- 初回リリース