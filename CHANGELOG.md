# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed (docs)
- `docs/ROADMAP.md` 第5章（プラグイン機構の運用負荷）を実態に更新。共有パッケージに `pkg/fsutil` を追加し、combined `go.work` と `-trimpath` の一致が必須であることを明記。再ビルド自動化（`build-all.sh` / `scripts/build-plugin.sh`）と `plugin.Open` 失敗の可視化（`reason_code`）は実装済みとしてチェックを付けた。
- プラグイン再ビルドの注意（`docs/PROCEDURE.md` 5.1 / 5.3）を README と揃えた。共有パッケージに `pkg/fsutil` を追加し、`go.work` と `GOWORK=off` の混在が build ID 不一致を招くこと、`-trimpath` を本体とプラグインで一致させること、プラグインビルド例に `-trimpath -buildvcs=false` を付けることを明記した。

### Fixed
- 配布中の `.deb` が陳腐化し得る問題を記録（`packaging/README.md` のリリース手順で対応）。実機の apt 検証で、`apt install` した host とリポジトリでビルドしたプラグインが `plugin was built with a different version of package Kizuna-Eye/pkg/status` でロード不能になった。原因は環境差ではなく、共有パッケージ（`pkg/status` 等）を変更した後に `.deb` を再ビルドしていなかったこと（`.deb` が 10/12、`pkg/status` 変更が 10/13）。**共有パッケージを変更したら `.deb` も再ビルドして APT リポジトリを更新する**必要がある。`.deb` を再ビルドしたところ、同一ソースのプラグインが署名検証付きで正常にロードされることを実機で確認した。
- `apt purge` が `/opt/kizuna-eye` と `/usr/lib/kizuna-eye` を残していた問題を修正した（`packaging/postrm.sh`）。postrm はファイル単位でしか消しておらず、dpkg は空でないディレクトリを残す（「not empty so not removed」）。`/opt/kizuna-eye` は bin のみ梱包で dpkg 管理外のため、`plugins/` が残ると空にならず消えなかった。purge 時に両ディレクトリを明示的に削除するようにした。
- 実機の apt 検証で見つかったログの未定義キーを修正した。`dash.api_insight`（`cmd/dashboard/main.go`）と `agent.module_status_sent` / `agent.module_status_failed` / `agent.plugin_name_panic` / `agent.sec_event_requeue_panic`（`cmd/agent/main.go`）が `pkg/logger/catalog.go` に無く、ログ本文に生キーがそのまま出ていた。ja/en 両方に追加した。あわせて、**コード中で使われている `*T` キーがカタログに存在するか**を走査する回帰テスト `TestCatalogCoversAllUsedKeys` を追加した（従来のパリティテストは ja/en の一致しか見ておらず、この種の抜けを検出できなかった）。
- deb の postinst が設定の `public_key_file` が指す `/etc/kizuna-eye/` を作っていなかった問題を修正した。`require_signature=false` の間は実害が無いが、後で署名検証を有効化すると鍵の置き場が無く fail-closed で起動拒否になる。postinst でディレクトリを作成し、公開鍵がパッケージに含まれていれば配置するようにした。
- `install.sh` の英語 UI（`UI_LANG=en`）で日本語のまま表示されていた 30 件のメッセージを `tr_msg` に追加した。対象はワンクリック対処サービス（特権ソケット）の導入、agent unit の最新化・稼働確認、H-1 移行の補助メッセージ、deb（apt）管理下の検出、プラグイン ABI 不一致の警告、プラグインソース欠落の案内など。あわせて、`go が見つかりません` / `x/sys 不一致` / `旧 unit ... を disable` の 3 パターンが既存の `tr_msg` で途中までしか英訳できておらず日本語の接尾辞が残っていた問題も直した。

## [0.8.0] - 2026-10-13

### Added
- セキュリティ状況（集計）API とダッシュボードの「セキュリティ状況」セクション（`pkg/insight` / `GET /api/insight`）。既存のアラート履歴を集計するだけで、新しい検知や永続データは増やさない（設計書 4.1「コアは観測と可視化に徹する」）。内容は3つ: ①対象期間の脅威指標（レベル別重みから 0..100 のリスク指数と A〜E 評価、寄与の大きい検知種別）②発生元の傾向（メッセージから抽出した接続元アドレスごとの件数・重大数・初回/最終時刻・単一/複数手法、時間帯ヒストグラム）③設定の点検（認証・署名検証・agent_token・履歴署名・通知・ループバック限定待ち受け等を重み付きで採点）。IP はメッセージ本文から抽出するため、断定ではなくヒントとして扱う（`net.ParseIP` で検証し、`0.0.0.0`/ブロードキャストは除外、`"12:34"` や素のポート番号は IP と誤認しない）。集計キーは英数字と `_` に正規化し、一覧は上限10件。設定の弱点（どの対策が無効か）を並べるため **operator 以上のみ**（公開ビューアには非表示・サーバーも 403）。UI 文言は i18n に置き、API は文字列を返さない。回帰テスト `pkg/insight/insight_test.go` / `internal/api/insight_test.go` を追加
- 週次セキュリティレポート（`pkg/report`＋ダッシュボードのスケジューラ）。アラート履歴（直近7日）から「概要・検知種別・重大アラート・改善提案」を Markdown で生成し、`notifications.weekly_report_enabled` を true にすると指定チャンネル（既定 email）へ毎週送信する。cron ライブラリは追加せず標準 `time` で実装。opt-in（既定 off）で、チャットチャンネルは汚さない。回帰テスト `pkg/report/report_test.go` を追加
- 設定のスナップショットとタイムトラベル（`pkg/configsnap`）。設定保存の直前に現行ファイルを `snapshots/<name>/`（0600、Samba 共有外）へ退避し、GFS 的（直近20 + 日次7 + 週次8 + 月次12）に世代管理する。`GET /api/config/{type}/snapshots` で一覧、`POST /api/config/{type}/restore` で任意時点へ復元（復元前も退避するため可逆）。`agent_config.json` / `dashboard_config.json` / `modules.json` が対象。回帰テスト `pkg/configsnap/configsnap_test.go` を追加

### Changed
- プログラムのコメントを「日本語（常体・〜だ/〜である）→英語」の2行ペアに統一した（Go / シェル / JS / CSS / systemd unit）。参照実装は `pkg/config/config.go`。
- 回帰防止チェッカー `scripts/check_comments.cjs` を追加した。`//`（Go）/`#`（sh）/`/* */`（JS/CSS）/`#`（systemd unit）に対応し、英語のみのコメントブロックを検出する（`--strict` で CI を落とせる）。CI（`.github/workflows/ci.yml`）に組み込み、Go / JS / シェル / systemd unit の4種を検査する。
- バージョン・フォールバック文字列を `v0.7.0` から `v0.8.0`（VERSION と `internal/api/version.go` に一致）へ更新した（`build.sh` / `install.sh` / `update.sh` / `scripts/build.sh` / `scripts/verify.sh` / `packaging/build-deb.sh`）。
- doc コメントの日英ブロック間に空行を入れ、`gofmt` を通した（`internal/api/plugins.go` / `internal/pluginsig/pluginsig.go` / `pkg/configsnap/configsnap.go` / `pkg/report/report.go`）。

- モジュール提供カードの iframe URL に現在のテーマ（`&theme=dark|light`）を付与し、テーマ切替時に iframe を再読み込みするようにした（`web/static/app.js`）。iframe は親の CSS 変数を継承しないため、テーマを無視したモジュール UI（背景を固定したもの）はライトモードでも暗いままになっていた。これでモジュール側が `theme` を読んで配色を切り替えられる。Kizuna-Security の Web UI 側で `theme` を解釈する対応は別途必要。
- モバイル UI の最適化（`web/static/modules.css` / `web/static/redesign.css`）。①モジュール管理のファイル操作（`.file-actions`）が 480px 以下で `display:flex` のまま `grid-template-columns` を指定しており（死んだ宣言）、意図した 2 列等幅になっていなかったため `flex: 1 1 calc(50% - 3px)` に修正。②スマホ横向き（`max-width: 960px and orientation: landscape`）ではゲージグリッドを 2 列に戻し、短い縦幅を有効に使う（縦向きは 1 列のまま）。③スマホ（≤768px）で表示が過大になる問題を修正。`redesign.css` は PC の読みやすさ優先で基準フォント 19px・カード余白 30px・ゲージ canvas max-width 320px と大きくしており、後読みのため `style.css` のスマホ向け縮小（`.gauge-card { padding: 12px 10px }` 等）を上書きしていた。ファイル末尾にスマホ専用のコンパクションブロックを追加し、基準フォント（768px=15px / 600px=14px / 420px=13px）・ヘッダー・カード・ゲージ・コアタイルを縮める。PC（>768px）には影響しない。
- ダッシュボードの稼働時間カードで、数字（`.uptime-value`）だけをヘッダーの「ダッシュボード」タブと同じアクセント色（`--primary-color`）にする。カード見出し「稼働時間」やメタ項目（起動日時/平均負荷/ネットワーク）は従来どおりの文字色のまま（`web/static/redesign.css`）
- 通知チャンネルの webhook が example のプレースホルダのまま有効な場合、保存時は検証エラー、ロード/実行時は警告してスキップする（Discord が HTTP 400 を返し続け、通知が一度も届かないまま再試行ストームになるのを防ぐ）
- `build-all.sh` を追加。共有パッケージ（`pkg/module` / `pkg/status` / `pkg/fsutil` 等）を変更した際に、host と全プラグインを一括でビルド・署名・配置する（片方だけ更新すると ABI 不一致で全プラグインがロード不能になる事故の恒久対策）
- プラグイン（モジュール）を本体リポジトリに同梱しない方針に変更。Kizuna-Security のソースは本体リポジトリ内 `plugins/Kizuna-Security/` から撤去し、各モジュール側（`/samba/share/Kizuna-Security` 等）で管理する。`install.sh` / `update.sh` / `scripts/build-plugin.sh` のプラグインソース既定パスを廃止し、`KIZUNA_SEC_PLUGIN_DIR` / `KIZUNA_LITE_PLUGIN_DIR` を指定したときのみ扱う opt-in に統一（マシン固有の絶対パスを埋め込まない）。`go.work` のプラグイン参照と `.gitignore` の plugins 固有行も削除

### Removed
- 古いインストーラの残骸 `install.sh.bak-20261008` を削除し、`.gitignore` に `*.bak` / `install.sh.bak*` を追加した。あわせてローカル資料（`Kizunaシリーズ　資料/`）を `.gitignore` に追加した。

### Fixed
- モジュール提供カード（iframe）がライトモードでも暗いままになる問題への対応。コアは iframe の URL に `&theme=dark|light` を付与し、テーマ切替時に再読み込みするようにした（`web/static/app.js`）。あわせて Kizuna-Security の Web UI（`webui.go`）が `?theme=` を読み、`html[data-theme]` に応じて背景・文字色を切り替える（ダーク `#17181c` / ライト `#ffffff`）ようにした。従来は `background:transparent` ＋ OS の `prefers-color-scheme` 依存で、ダッシュボードのテーマに追従しなかった。
- **プラグイン ABI の重大な落とし穴（-trimpath）**: ホスト（agent / plugin-inspect）は `-trimpath` 付きでビルドされているため、プラグイン `.so` を `-trimpath` **なし**でビルドすると標準ライブラリ（`cmp` 等）のビルドハッシュが食い違い、`plugin was built with a different version of package cmp` でロードに失敗する（実機で発生）。プラグインは必ず `go build -trimpath -buildvcs=false -buildmode=plugin` でビルドする（`build-all.sh` / `scripts/build-plugin.sh` / `update.sh` は対応済みだが、手動ビルド時は要注意）。
- スマホでアカウントメニュー（アバターのドロップダウン）が画面左端からはみ出して切れる問題を修正（`web/static/auth-guard.js`）。ドロップダウンは `right:0` でアバターに固定されるため、ヘッダーが折り返してアバターが画面左寄りになると 260px 幅のメニューが左端を越えていた。開いた直後に実測し、左端が画面外ならアンカーを左（`left:0` / `right:auto`）へ反転し、幅をビューポート内にクランプする `clampDropdown` を追加。
- スマホでダッシュボードのカード／セクションの上を指でなぞると、ページがスクロールせず並べ替えが始まる（スクロールが止まる）問題を修正（`web/static/app.js`）。カード並べ替えの `onDown` が「カード本体のどこを触っても」`preventDefault()` と `setPointerCapture()` を呼んでいたため、カードの余白を触った時点でスクロールが奪われていた（モジュール管理の `modules.js` は元からハンドル限定で、ダッシュボードだけが本体ドラッグを許していた）。タッチ/ペンでは明示的なつまみ（`.drag-handle`）からのみドラッグを開始し、マウスは従来どおりカード本体からも開始できるようにした（PC の UX は不変）。
- ログのリモート転送（`pkg/logger/forward.go`）で、`forwardWriter.Close` と `Write` が並行したときに send-on-closed-channel で panic し得る競合を修正。直前の追加では `closed atomic.Bool` を「確認してから送信」の間に置いていたため、その隙間に `Close`（`closed` セット＋`close(ch)`）が入ると Write が閉じたチャネルへ送って panic する TOCTOU が残っていた（logger 経由では Write/Close が直列化されるため未到達だが、writer 単体の将来の誤用に脆い）。`sendMu sync.RWMutex` を追加し、Write は read ロックの下で「closed 確認＋送信」を一体で行い、Close は write ロックの下で `closed` セットと `close(ch)` を行う。Write は `select` の `default` でブロックしないため Close が飢餓になることはない。並行 Close/Write の回帰テスト `TestForwardWriterConcurrentCloseWrite` を追加（`-race` で検出）
- `docs/ROADMAP.md` の「インストーラは systemd 保護（ProtectSystem / NoNewPrivileges / AmbientCapabilities）で最小権限動作」という記述を、実態に合わせて修正。agent unit は全保護を適用するが、dashboard unit はワンクリック対処（`sudo -n` ヘルパー）と両立させるため `NoNewPrivileges` / `ProtectSystem=strict` / `PrivateTmp` を適用しない（残りの保護は適用）ことを明記
- `install.sh` がワンクリック対処ヘルパー（`kizuna-action.sh`）の sudoers を **agent 用ユーザー `kizuna-eye`** に付与していたため、**dashboard を実行ユーザー（運用ユーザー）で動かす install.sh 経路では管理者のワンクリック対処（kill / ファイアウォール / cron 削除 / 復元）が全て失敗**していた問題を修正（`install.sh`）。dashboard は対処ヘルパーを `sudo -n` で実行するが、install.sh は dashboard を運用ユーザーで動かす（`systemd/kizuna-dashboard.service` の `__USER__`=`RUN_USER`）ため、付与先は dashboard の実行ユーザーでなければならない。deb 経路は postinst が dashboard を `kizuna-eye` で導入するので `PKG_USER` 付与のままで正しい（2 経路で正当に異なる）。cron / smartctl ヘルパーは agent（`kizuna-eye`）が実行するため `kizuna-eye` のまま
- dashboard unit の権限分離が、管理者向け「ワンクリック対処」（`internal/api/actions.go` が `sudo -n /usr/local/bin/kizuna-action.sh` を実行）を機能的に不能にしていた問題を修正（`systemd/kizuna-dashboard.service`）。`NoNewPrivileges=yes` は sudo の setuid 昇格を禁止し（実測: `systemd-run --property=NoNewPrivileges=yes sudo -n ...` が「The "no new privileges" flag is set, which prevents sudo from running as root.」で失敗）、`ProtectSystem=strict` は cron spool への書き込みを、`PrivateTmp=yes` は実 tmp への復元を、それぞれ阻害していた。dashboard ではこの 3 点のみ適用しない（`ProtectHome` / `ProtectKernelTunables` / `ProtectControlGroups` / `RestrictRealtime` / `RestrictSUIDSGID` は維持）。agent は sudo を使わずケーパビリティで直接実行するため `NoNewPrivileges=yes` を維持できる。**その後、ワンクリック対処を特権ソケットサービス（`kizuna-action.socket` / `kizuna-action.service`、systemd socket activation）へ委譲し、dashboard は sudo を必要としなくなった。これにより dashboard も agent と同じく全保護（`NoNewPrivileges` / `ProtectSystem=strict` / `PrivateTmp` を含む）を適用するようになった。本エントリはその時点（3 点のみ除外）の記録であり、現状は上記のとおり全保護である（`docs/ROADMAP.md` / `packaging/RELEASE_NOTES_v0.8.0.md` を参照）。**
- プラグイン（`.so`）が `Name()` / `RequeueSecurityEvents` でパニックすると agent プロセス全体が落ちていた問題を追加修正（`cmd/agent/main.go`）。agent は `Register` の後にも `pluginMod.Name()` を呼び、無効化ブランチの名前解決でも `mod.Name()` を呼んでいた（いずれも回復の外側）。さらに `sendSecurityEvents` は agent のメイン goroutine（send ループ）で実行され、配信失敗時に `SecurityEventRequeuer.RequeueSecurityEvents` を呼ぶが、これも回復の外側だった。`safeModuleName` / `safePluginName` / `requeueSecurityEventsSafe` を追加し、いずれも panic を吸収する（回帰テスト `cmd/agent/panic_safety_test.go`）
- プラグイン（`.so`）が `Name()` または `Interval()` でパニックすると agent プロセス全体が落ちていた問題を修正（`pkg/module/base.go`）。これらは `moduleManager.Register` / `Start` / `runModuleLoop` から**パニック回復の外側**で呼ばれており、サードパーティ製 `.so` の `Name()`/`Interval()` のパニックが未回復のままプロセスを終了させ、他の全プラグインとシステム監視を巻き添えにしていた（設計原則「プラグインのパニックで agent を落とさない」に反する）。`safeName` / `safeInterval` ラッパーを追加し、`Register`（空名は拒否）、`Start`、`runModuleLoop`、および既存の `safeInit` / `safeRun` の recover ハンドラ内（recover 中の `Name()` 呼び出しは未回復で落ちるため）でこれらを使用する。回帰テスト `pkg/module/panic_names_test.go` を追加

- `scripts/build-plugin.sh`（単一プラグインのビルド→署名→配置）も `GOWORK=off` でビルドしていたため、go.work でビルドした稼働中の agent に対して `plugin was built with a different version of package Kizuna-Eye/pkg/fsutil` でロードできない .so を生成していた。ホストリポジトリとプラグインのディレクトリを列挙した combined go.work の下でビルドするよう修正。
- `update.sh` にも `build-all.sh` と同じ go.work / replace 混在の ABI 不一致があったため、ホストと全プラグインを 1 つの combined go.work の下でビルドするよう修正（`-trimpath` も付与して他スクリプトと統一）。これが無いと `update.sh` 実行後に全プラグインが `plugin was built with a different version of package Kizuna-Eye/pkg/fsutil` でロード不能になる。
- （Kizuna-Backup LITE）バックアップ実行中の状態（`running`）がパニック等で解除されず、以後すべての実行が「バックアップ実行中」で恒久的に拒否される問題を修正。`Run()` は `moduleManager.safeRun` 経由で呼ばれ、プラグイン内のパニックは回復されるが `finish()` に到達しないため `running` が残る。実行開始時刻を保持し、`runStaleTimeout`（6時間）を超えた `running` は異常終了とみなして解除する（ゼロ値も stale 扱い）。回帰テスト `stale_test.go` を追加
- `build-all.sh` がホストを **go.work 有効**で、プラグインを **GOWORK=off**（=プラグイン側の `replace Kizuna-Eye => ...`）でビルドしていたため、共有パッケージ（pkg/module・pkg/status・pkg/fsutil）の build ID がホストとプラグインで食い違い、`plugin was built with a different version of package Kizuna-Eye/pkg/fsutil` で .so がロードできない問題を修正。ワークスペースと `replace` は同じソースを指してもモジュールグラフのハッシュが異なるため、同一ツールチェーン・同一 `-trimpath` でも一致しない（実機 2026-10-10 で発生）。ホストと全プラグインを **1つの combined go.work** の下でビルドするよう変更（`use` にホスト＋各プラグインディレクトリを列挙、一時ファイルは EXIT トラップで削除）。
- （Kizuna-Backup LITE）`CreateArchive` がディレクトリ走査中に開いたファイルを `defer` で閉じていたため、walk が終わるまで全ファイルの fd を保持し、大量のファイルを持つツリーで fd を使い果たしうる問題（EMFILE）を修正。通常ファイルのみを開き、`io.Copy` 後に明示的に閉じる。あわせてシンボリックリンクをリンク先へ辿らずリンクとして記録し（対象外のファイル取り込み・リンクループを防止）、FIFO/デバイス/ソケットは内容を書かずヘッダのみにする（FIFO の open はブロックしうる）。アーカイブ出力も `O_NOFOLLOW` で開き、共有 temp ディレクトリに事前に置かれた symlink を辿って任意ファイルを切り詰める CWE-59 を塞ぐ。回帰テスト `archive_test.go` を追加
- `scripts/check_i18n.js` が `web/static` 配下のサブディレクトリ（`fonts/` 等）を `readFileSync` で読み、`EISDIR` で異常終了していた問題を修正。ディレクトリ・非通常ファイルをスキップする。`verify.sh` / CI の i18n チェックが常に失敗していた（`OK: 374 used / 547 ja / 547 en keys — all consistent` を確認）
- サードパーティモジュールの sandbox ラッパーに、モジュールが申告した `web_ui_path` を無害化せず `<iframe src="...">` 属性へ埋め込んでいた問題を修正（格納型 XSS）。`"` を含む entry を申告した悪意ある .so が、ダッシュボードと同一オリジンの wrapper 内で属性を閉じて `onload=` 等を注入できた。`safeWebUIEntry` で HTML メタ文字（`< > " ' & \``）を拒否して `index.html` へ fail-closed し、さらに `html.EscapeString` で二重に無害化する。回帰テスト `internal/api/plugins_webui_entry_test.go` を追加
- `plugin-inspect` の Web UI 展開が `os.WriteFile` を使っており、展開先（`pluginsDir` 配下、agent ユーザーが書き込み可能）に置かれた symlink を辿って任意ファイルを上書きしうる問題（CWE-59）を修正。`fsutil.WriteFileAtomic`（symlink 拒否＋fsync＋rename）に統一。`pluginsig.SignFile` / `plugin-sign -gen-key` / ダッシュボードのアップロード経路と同水準に揃えた
- 認証ミドルウェアの静的アセット公開リストに `.otf` / `.eot` を追加。ロゴ用フォント（`fonts/LogotypeGothic.otf`）が未ログイン時に 302 でブロックされ、公開ビューアやログイン画面でロゴフォントが読み込まれなかった問題を修正（回帰テスト `internal/auth/middleware_assets_test.go` を追加）
- 隔離 agent（A-4: 専用ユーザー + `NoNewPrivileges=yes`）で S.M.A.R.T が無言で欠落していた問題を修正。実測で `CAP_DAC_READ_SEARCH` だけでは `smartctl` の ATA パススルーが `Operation not permitted` になり、ディスク健康状態・温度・書込量・通電時間が取得できていなかった。unit に `CAP_SYS_RAWIO` を追加し、実際に読み取れたかを起動時と収集時に確認して、失敗する場合は一度だけ警告する（`smartctl --scan` は権限が無くても成功するため可用性判定に使えなかった）
- `block_mode=enforce` の自動 IP ブロックが `NoNewPrivileges=yes` で必ず失敗していた問題を修正。sudo は no-new-privileges 下で確実に拒否される（実測: `sudo -n true` が `status=1/FAILURE`）ため、firewall 操作は対象バイナリを直接実行する経路を先に試し、失敗時のみ `sudo -n` にフォールバックする。unit に `CAP_NET_ADMIN` を追加（実測で kizuna-eye が `nft add` に成功）。起動時に firewall へ到達できるかを確認し、enforce なのに操作できない場合は警告する
- `apt`（.deb）経路で特権ヘルパーが導入されず、ワンクリック対処が全て失敗していた問題を修正。`.deb` に `kizuna-action.sh` / `kizuna-cron-read.sh` を同梱し、`postinst` で `/usr/local/bin` へ配置して sudoers（`visudo -cf` 検証付き）を生成、`prerm`/`postrm` で確実に除去する
- ダッシュボード API がログの書式キーを書式文字列として使い、ログに `%!(EXTRA ...)` が混入していた問題を修正（`pkg/logger` の `LogInfo`/`LogWarn`/`LogError`/`LogDebug` がカタログ値を描画する）。ja/en カタログのキー一致を Go テストで固定
- `plugins` の署名検証を `require_signature=false` でも行うよう修正。従来は鍵が設定されていても検証自体が走らず、正しく署名された純正プラグインまで `origin=third_party`（赤アラート・未検証バッジ・sandbox iframe）になっていた。origin はファイルの性質として判定する
- プラグイン署名ファイル（`.so.sig`）と秘密鍵の書き込みを symlink 非追従に（`plugin-sign -gen-key` は `O_EXCL` / atomic、`pluginsig.SignFile` は `fsutil.WriteFileAtomic`）
- モジュールの `type` 変更（`plugin` 以外へ）を admin 限定に。agent がロードをスキップするため、無効化と同じ保護能力の変更にあたる
- `/api/metrics` が public viewer にプロセス一覧・S.M.A.R.T（health/temp/mount）を開示していた問題を修正（`/api/status` と同じサニタイズを適用）
- `plugins.require_signature=true` かつ `public_key_file` 未設定の設定を API が保存でき、次回起動でサービスが fail-closed 停止していた問題を修正（保存時に 400 で拒否）
- EN モードでアラート履歴が日本語のままだった問題を修正。ダッシュボード自身が生成するアラート（メモリ/ストレージ/CPU温度/ディスクS.M.A.R.T/Agent切断・復帰・未接続、プラグイン監査、認証イベント）に英語文言が無く、`title_en`/`message_en` が空だった。`notify.Alert` に `TitleEN`/`MessageEN` を追加し、履歴保存時に詳細または Alert 本体の EN を採用（`firstNonEmpty`）。監査/認証フックも EN 詳細を渡すよう拡張。これで UI 言語に応じて `pickLocalized` が英語を表示する（Kizuna-Security のイベントは元々 EN 付き）
- ダッシュボードのモジュールステータス（プラグインカード）が点滅して見える問題を修正。毎秒の status 更新のたびに `renderPluginList` が `innerHTML` を差し替えてカード DOM を作り直し、CSS アニメーション（pulse 等）がリセットされていた。生成 HTML が前回と同一なら書き換えないようにし、ホバー（`translateY(-2px)`）などの見た目は維持する
- モジュール管理画面がロード失敗を反映していなかった問題を修正。Agent の `/api/plugin-runtime`（および WS `module_status`）を読み、`status=failed` のモジュールは「有効」ではなく赤い「読み込み失敗」バッジと失敗理由を表示する（従来は is_backup=false を「監視中」と誤表示していた）
- ダッシュボードのモジュールカードで、ロード失敗理由のツールチップが英語 UI でも日本語のままだった問題を修正。既知の `reason_code` は `plugin.fail.*` で翻訳し、未知コード／旧 Agent の生メッセージは UI が日本語のときだけ表示、それ以外は翻訳済みの汎用メッセージに置換
- Agent が `plugin_path` 不正時に `reason_code` を送っていなかった問題を修正（`invalid_plugin_path` を付与し UI で翻訳可能に）
- `DELETE /api/modules/{name}`（モジュール管理画面の「削除」が使う経路）が `.so` を消す際に分離署名 `<name>.so.sig` を残していた問題を修正。残った古い署名は、同じ名前で別の `.so` を後からアップロードしたときに署名検証と一致せず、`require_signature=true` 環境でプラグインが静かにロード拒否される原因になる（`internal/api/modules.go` は `pluginsig.SigPath` で署名も削除。アップロード経路 `PluginManager.handleDelete` と挙動を揃えた。回帰テスト `internal/api/modules_delete_sig_test.go` を追加）
- `internal/api/version.go` の `Version` フォールバックが `v0.7.0` のまま `VERSION`（v0.7.1）とずれていた問題を修正（ldflags 無しビルドでのみ使われる値。`VERSION` と一致させた）
- アップロードした `.so` のファイル名とプラグイン自身の `Name()` が異なる場合、同梱 Web UI の展開先が `<アップロード名>.web` のままで、`/plugins/<canonical>/` と `HasWebUI`（いずれも `meta.Name` 基準）が一致せず**カードの iframe が 404** になっていた問題を修正。`reconcileWebUIDir` で展開先を canonical 名へリネームし、`<アップロード名>.web` が削除時に孤児として残る問題も解消（回帰テスト `internal/api/plugins_webdir_rename_test.go` を追加）
- サードパーティモジュールの Web UI が**必ず 404** になり、設計書 5.4 の「sandbox iframe で隔離表示」が実質機能していなかった問題を修正。ラッパーは `/plugins/<name>/_asset/index.html` を参照するのに対し、`plugin-inspect --web-out` は実体を `baseDir` 直下（`_asset/` なし）へ展開していたため一致していなかった。`handleWebUI` が `_asset/` 接頭辞を剥がして実体へ解決し、`http.ServeFile` の「`/index.html` を 301 リダイレクトする」特殊挙動を避けるため `ServeContent` で配信するよう変更（symlink/包含チェックは維持）。回帰テスト `internal/api/plugins_webui_asset_test.go` を追加
- ビルドスクリプト（`build.sh` / `build-all.sh` / `scripts/build.sh` / `scripts/build-plugin.sh`）が `-trimpath` を意図的に外していたため、**ビルドしたユーザーの GOMODCACHE 絶対パスがパッケージの build ID に埋め込まれ**、agent を `sudo`（`/root/go`）で、plugin を user（`/home/user/go`）でビルドしただけで `plugin was built with a different version of package github.com/shirou/gopsutil/v3/common` でロード不能になっていた（実機で発生）。`-trimpath` を **host と全 plugin で常時 ON** に統一し、build ID をパス非依存にした（片方だけ付けると壊れるが、両方に付ければ誰がビルドしても一致する）。`docs/DESIGN_PRINCIPLES.md` の方針記述も更新
- `build.sh` を `sudo` で実行すると `/opt/kizuna-eye/bin/` のバイナリが root:root 0700 になり、agent（kizuna-eye ユーザー）が `203/EXEC` で起動不能、dashboard も failed になる問題に対し、`build-all.sh` の注意喚起を強化（ビルドは user、配置は sudo を推奨。`-trimpath` 統一により sudo ビルドでも ABI は一致する）
- `pkg/insight` の `ExtractIPs` が正規表現ベースで、5MB 級の行（16進文字の連続）に対して `-race` 下で 5 秒を超えていた問題を修正。線形スキャナ（IP 形状バイトの最長ランを走査し、`net.ParseIP` は候補ごとに1回だけ呼ぶ）に置き換え、二重パースも解消。約 5.6MB 入力で 0.01 秒。回帰テストを追加・閾値を 10 秒に（回帰テスト `pkg/insight/insight_robust_test.go`）
- ダッシュボードのモジュールカードが `DashboardCard.min_role`（設計上の契約）を尊重していなかった問題を修正。`app.js` は未定義の `currentRole`（modules.js の変数）を参照していたため、min_role 判定が常に不一致となり**カードが一切マウントされない**状態だった。app.js 専用の `accessRole` を導入し、`checkAccessMode`→`applyAccessMode` で設定、`declaredModuleCards` が min_role を rank 比較（不明値は fail-closed）で判定してからマウントする。`applyAccessMode` の末尾で `syncModuleCards()` を呼び直し、権限判定前にカード取得が走っても最終的に正しく反映される
- サードパーティの sandbox ラッパーが、ダッシュボードが付けた `?lang=<ui>` を内側 iframe に伝えていなかったため、**サードパーティのカードだけ UI 言語が反映されなかった**問題を修正。`serveSandboxWrapper` に言語を渡し、`ja`/`en` のみ受理（ホワイトリスト）して内側 iframe の URL と `<html lang>` に使う（ホスト言語値の反射なし。回帰テストで確認）
- `packaging/apt-repo.sh` が `KIZUNA_APT_REPO_DIR` を検証せず `rm -rf "$REPO_DIR"` を実行していた問題を修正。空・`/`・ホーム・リポジトリ直下を拒否し、書き込み可能な親の存在も確認する安全ガードを追加（`uninstall.sh` と同種。誤設定で `rm -rf /` を踏まない）
- モジュール Web UI の配信で `Content-Type` を無条件に `text/html` にしていたため、同梱の CSS/JS/フォントが誤った型で配信され、`X-Content-Type-Options: nosniff` によりブラウザが適用を拒否していた問題を修正。拡張子から `mime.TypeByExtension` で型を決め、未知の拡張子のみ `text/html` にフォールバックする（回帰テストで CSS の型を確認）
- **純正**モジュールの Web UI も `frame-ancestors 'none'`（`X-Frame-Options: DENY`）で配信されていたため、ダッシュボードの同一オリジン iframe（モジュールカード）に埋め込めず**カードが空白**になっていた問題を修正。純正・サードパーティとも同一オリジンの親（ダッシュボード）にのみ埋め込みを許可する（`X-Frame-Options: SAMEORIGIN` / `frame-ancestors 'self'`）。クロスオリジンの埋め込み（クリックジャッキング）は引き続き拒否する。回帰テストで確認
- サードパーティの sandbox ラッパー自身が `X-Frame-Options: DENY` かつ `frame-ancestors` 未指定（CSP `default-src 'none'` へフォールバック＝`'none'`）だったため、**ダッシュボードの同一オリジン iframe に埋め込めず、カードが空白**になっていた問題を修正。ラッパーは同一オリジンの親にのみ埋め込みを許可する（`X-Frame-Options: SAMEORIGIN` / `frame-ancestors 'self'`）。実体 UI の隔離は内側の sandbox iframe が担う（回帰テストで確認）
- サードパーティの sandbox ラッパーが、モジュールが `WebUIPath()` で申告した entry を無視して `index.html` 固定で参照していた問題を修正（設計書 5.4「カードはモジュールが追加する」に反する）。`safeWebUIEntry` で `meta.json` の `web_ui_path` を検証（絶対パス・`..`・`\` を拒否、既定 `index.html`）してから iframe の `src` に使う。回帰テスト `internal/api/plugins_webui_entry_test.go` を追加
- `install.sh` / `CONTRIBUTING.md` の Go 要件が `1.27.1` のまま `go.mod` の `toolchain go1.27.2` / CI / README とずれていた問題を修正。1.27.1 は到達可能な net/http 脆弱性を含む版であり、またホストと全プラグインを同一パッチでビルドしないと ABI 不一致でロード不能になるため、要件表示を 1.27.2 に統一

### Security
- 通知webhookのプレースホルダ検出を強化（`pkg/config/urlcheck.go`）。ダミーの Discord webhook（`000000000000000000` / `xxxxxxxxxxxxxxxxxxxxxxxx`）は文字列マーカー（YOUR_WEBHOOK 等）を含まないため、プレースホルダとして検出されず、`enabled: true` のまま通知が HTTP 400 で無言で失敗し続けていた（設計原則「無言の失敗を許さない」に反する）。Discord の `/api/webhooks/<id>/<token>` の各セグメントが同一文字の連続（12文字以上）なら偽物とみなし、プレースホルダとして扱う（ロード時に警告し、通知はスキップ）。回帰テスト `pkg/config/urlcheck_test.go` を追加
- ログのSSEストリーム（`GET /api/logs/stream`）に `X-Content-Type-Options: nosniff` を追加し、非ストリームの `GET /api/logs` と揃えた。ログ行は攻撃者が制御しうる文字列（ユーザー名・パス）を含むため、内容が別の型としてスニッフィングされないことを明示する（`internal/api/logs.go`）
- アバター配信（`internal/auth/avatar.go`）がシンボリックリンクを追従していた問題を修正（CWE-59）。`handleAvatarServe` は `http.ServeFile` に生パスを渡しており、アバター用ディレクトリに置かれた symlink（`evil.png -> /etc/passwd`）を辿って対象ファイルの内容を返し得た。この endpoint は `<img>` タグ用に意図的に公開されているため、任意ファイル漏洩につながる。`logs.go` / `plugins.go` と同じく `EvalSymlinks` で実体解決し、アバター用ディレクトリ配下であることを再確認、非通常ファイル（デバイス/ディレクトリ）も拒否する。回帰テスト `TestHandleAvatarServeRejectsSymlink` を追加。あわせて、アバター差し替え/削除時の旧ファイル削除に `safeAvatarBaseName` を適用し、破損・改ざんされた `users.json` の値（`../../etc/passwd` 等）でアバター用ディレクトリ外を削除できないようにした
- ワンクリック対処の特権実行（`internal/api/actions.go`）が `sudo` を裸名で `exec` していた問題を修正（設計原則 V3: 特権ヘルパーの絶対パス化との不整合）。PATH にユーザー書き込み可能なディレクトリがあると偽 `sudo` を実行させる余地があった。`/usr/bin/sudo` 等の既知の絶対パスを優先し、無ければ `LookPath` にフォールバックする `resolveSudoBin()` を追加して使用する（`pkg/module` の smartctl・`internal/updater` と同じ方針に統一）
- 設定エディタの `public_viewer` チェックボックスの未設定時既定を **config_version 連動**にした（バックエンドの `LoadDashboardConfig` と同じ規則）。固定 ON のままだと、セキュア既定（`config_version>=2`）の環境で他の設定を保存しただけで `public_viewer=true` が明示書き込みされ、匿名閲覧が静かに再有効化される（セキュア既定の後退）ため。`web/static/config-editor.js`。
- `SECURITY.md` の「危険なデフォルト（見直し計画中）」を実態に合わせて更新（新規は secure 既定、既存は現状維持、`listen_addr`/`secure_cookies` の扱いを明記）
- 設定のセキュアデフォルト化（`config_version` 導入）。`DashboardConfig` / `AgentConfig` に `config_version` を追加し、**新規インストール（設定ファイル無し、または `config_version>=2`）は `public_viewer=false`** を既定にする（read-only ビューア用エンドポイントをログイン必須にする）。**既存インストール（`config_version` 無し）は従来どおり `public_viewer=true`** を維持し、アップグレードで挙動が変わらない。明示された `public_viewer` は常に尊重する。配布用 `*.example.json` に `config_version: 2` を付与。回帰テスト `pkg/config/config_secure_defaults_test.go` を追加
- Go ツールチェーンを go1.27.2 に固定（`go.mod` / `go.work` の `toolchain` と各ビルドスクリプト）。go1.27.1 は net/http の HTTP/2 に到達可能な脆弱性を10件含み（HPACK エンコーダ競合・Trailer によるメモリ枯渇など、GO-2026-6603〜6617）、govulncheck で検出されていた。go1.27.2 でこれらは解消する。Go プラグインは共有パッケージのビルドハッシュ完全一致が必要で、パッチが違うと `plugin was built with a different version of package` で全 .so がロード不能になるため、ホストと全プラグインを同一ツールチェーンで同時再ビルド・再署名することを必須とし、スクリプト（build.sh / build-all.sh / scripts/build*.sh）に固定を組み込んだ。CI も 1.27.2 に更新
- operator の権限範囲を明確化（コード実行境界を除く）。`docs/PROCEDURE.md` / `README.md` / `README.ja.md` に operator と admin の境界（コード実行境界を越えるか）を明記
- モジュール設定のうち保護・復旧・コード実行境界に直結するフィールドの変更を admin 限定に（fail-closed）。対象: `plugin_path` / `chain_key_path` / `integrity_baseline_path` / `integrity_files` / `watch_files` / `block_mode` / `fim_watch` / `fim_watch_paths` / `fim_watch_baseline_path` / `suid_check` / `suid_paths` / `suid_watch` / `suid_watch_paths` / `suid_fast_check` / `suid_fast_paths` / `cron_check` / `cron_paths` / `listen_port_check` / `firewall_backend` / `block_state_path` / `block_whitelist` / `block_duration_sec` / `sudo_ignore_commands` / `notify_min_level` / `log_path`。`PUT /api/modules/{name}` で現在値と異なる場合に admin を要求（`internal/api/modules.go`）。admin 判定は `auth.Handler.IsAdmin` を注入（api は auth に非依存）。しきい値・間隔・除外などの運用調整は operator のまま
- アラート履歴のクリア（`DELETE /api/alerts`）を operator から **admin** に引き上げ（監査証跡を消す操作のため）

## [0.7.0] - 2026-10-06

### Added
- ログイン画面に「ゲストとしてログイン」ボタンを追加。`auth.public_viewer` 有効時のみ表示され、viewer 権限（読み取り専用）でダッシュボードに入れる
- ダッシュボードのカード（CPU/メモリ/ストレージ/稼働時間）をドラッグで並べ替え可能に（順序は localStorage に保存）
- ストレージカードをクリックすると CrystalDiskInfo 風の詳細モーダルを表示（健康状態・温度・型番・シリアル・総書込量・通電時間・使用率）
- ディスク S.M.A.R.T 情報を拡張（`smartctl` から型番・書込量・通電時間・回転数を取得。ATA/NVMe 両対応）
- WebSocket 切断時に「切断されました」ポップアップ、再接続時に「再接続しました」を表示
- 自動更新チェック（`internal/updater`）。GitHub Releases を監視し、新バージョン検知時に `safe_update.sh` を実行
- `install.sh` / `update.sh` / `safe_update.sh` を追加。必要なパッケージ（rsync / smartmontools / git / curl）を差分で確認・導入し、更新失敗時はロールバック
- `uninstall.sh`: `install.sh` が導入した systemd サービス・unit・sudoers・ヘルパー・/etc/kizuna-eye・sysctl 設定を撤去する。`--purge` でデータ・鍵・バイナリ・専用ユーザーまで完全削除、`--dry-run` で内容確認のみ
- `install.sh` にプラグイン署名ステップを追加（純正プラグインのみ。署名鍵が無ければ生成し `plugin-sign -sign-all`）
- `install.sh` に systemd 登録（dashboard + agent）を内蔵。`--no-systemd` で手動管理に切替可
- `update.sh` にプラグイン再署名ステップを追加（再ビルド後に `plugin-sign -sign-all`）
- `cmd/plugin-sign`: Ed25519 鍵対生成 / `.so` への分離署名 / 検証 / 一括署名（`-gen-key` `-sign` `-sign-all` `-verify` `-print-public-key`）。`build.sh`・`install.sh`・`update.sh`・`safe_update.sh`・`scripts/build.sh`・`scripts/verify.sh` のビルド/ロールバック対象へ組み込み
- `install.sh` の必須パッケージに `bubblewrap` を追加（A-3 の隔離検査で使用。無い場合は検査が fail-closed で失敗するため）
- ログイン / 初期セットアップ / ユーザー管理画面にダーク・ライトテーマ切替を追加（ダッシュボードと同じ `kizuna-theme` を共有、OS 設定にも追従）
- 認証・ユーザー管理（任意、`auth.enabled` で有効化）
- Agent の WebSocket トークン認証（`auth.agent_token` と `agent_config.json` の `token`）
- ヘッダー右上にユーザー名とログアウト、管理者には「ユーザー」リンクを表示
- `dashboard_config.json` の `log_level` を反映（`debug` / `info` / `warn` / `error`、未設定時は従来どおり `debug`）
- 全ページのヘッダーに接続ステータスバッジを追加（状態に応じて緑/黄/赤の色ドットで表示）

### Changed
- スクリプトの shellcheck 指摘を解消（`ls | grep` の廃止、未使用ループ変数、`cd ... || exit`、`/proc` の PID 列挙を glob 化）
- バージョン表記を v0.7.0 に統一（web 全体・Makefile・build.sh）
- アカウントメニューの絵文字を削除（アイコン変更 / パスワード変更 / ユーザー管理）
- CPUカードはCPU温度のみ、メモリカードは空き容量のみ、ストレージカードは温度＋空き容量のみを表示
- `install.sh` 一本で初期セットアップが完結（systemd 登録まで自動）。agent は専用ユーザー `kizuna-eye` で起動
- `update.sh` / `safe_update.sh` の systemd 検出を `kizuna-eye-agent` / `kizuna-dashboard` 対応にし、再起動・ロールバックを検出 unit で実施
- `start.sh` / `stop.sh` の案内メッセージを `kizuna-eye-agent` に統一
- `systemd/kizuna-dashboard.service` に `SupplementaryGroups=kizuna-eye` を追加（H-1 後の共有ログを dashboard が読めるように）
- 旧 systemd 補助スクリプト `systemd/install-services.sh` / `systemd/migrate-to-systemd.sh` と旧 unit `systemd/kizuna-eye.service` / `systemd/kizuna-agent.service` を削除（install.sh / migrate-agent-user.sh に一本化）
- コメント内の旧ユーザー名 `kizuna-agent` を `kizuna-eye` に統一
- agent 専用ユーザーを `kizuna-agent` から `kizuna-eye` に、agent の systemd unit を `kizuna-eye-agent.service` に変更（H-1 命名統一。コード完了・実機反映は別セッション）。既存の `kizuna-eye.service`（start.sh を呼ぶ旧 system service, Type=oneshot）とは別物で、名前衝突を避けるため agent 版は `kizuna-eye-agent.service` とした
- `migrate-agent-user.sh` / `start.sh` / `stop.sh` / `kizuna-watchdog.sh` の systemd 検出・unit 名を `kizuna-eye-agent` に対応
- plugin `kizuna_security` の cron 監視を sudo ヘルパーから直接読み取りへ変更（`AmbientCapabilities=CAP_DAC_READ_SEARCH` で `/var/spool/cron/crontabs` を直接読む。sudo/sudoers 不要。sudo 失敗時は従来のヘルパーへフォールバック）
- `LoadOrCreateAlertHistoryKey` の鍵作成モードを 0640、keys ディレクトリを 0750 に変更（A-4/H-1 で agent ユーザーが署名鍵を読めるように）
- `start.sh` / `stop.sh` の systemd 検出を `kizuna-eye-agent` / 旧 `kizuna-agent` の両対応に更新
- `.gitattributes` を追加して改行コードを LF に正規化（gofmt の安定化）
- README（英/日）を実装に合わせて全面的に整理（壊れていたコードフェンスと古い設定例を修正）
- `scripts/verify.sh` は gcc 未検出時に `-race` を「失敗」ではなく「スキップ」に変更
- `start.sh` / `stop.sh` に操作対象の引数（`all` / `dashboard` / `agent`、省略時は従来どおり両方）を追加。agent が systemd 管理下（`kizuna-eye-agent` / 旧 `kizuna-agent` が active または enabled）のときは agent を起動・停止しない（A-4 移行後の二重起動防止。強制する場合は `KIZUNA_FORCE_MANUAL=1`）
- `stop.sh` が kill の完了を確認せず「停止しました」と表示していた問題を修正。停止できなかった場合は警告を出して終了コード 1 を返す（別ユーザー所有のプロセスは EPERM で kill できないため）

### Removed
- 内部記録（作業記録・計画書・プラグイン監査記録）を `Kizunaシリーズ　資料/` へ退避し、公開リポジトリから削除。`docs/` は `PROCEDURE.md` / `ROADMAP.md` のみに整理
- 作業用スクリプト `.py` を全削除（`scripts/archive/patch_*.py`, `scan_emoji.py`, `ssh_run.py`, `test_ssh_fail.py`）
- 未使用の `parseSize` 関数（`cmd/agent`）
- 未使用パッケージ `internal/transport`・`internal/collector`・`internal/render`（本番コードから参照なし。Agent/Dashboard は `cmd/*/main.go` に直接実装）
- `notify.EmailNotifier` の未使用フィールド `useTLS`（`smtp.SendMail` が STARTTLS を自動交渉するため不要）

### Fixed
- Discord 通知の再送既定値が `0`（再送なし）として読まれ、HTTP 429 のアラートが黙って捨てられていた問題を修正。`discord_max_retries` を未設定（既定 5）と明示的な `0` で区別し、送信タイムアウトを再送回数に応じて延伸（15 秒でバックオフを打ち切っていた）
- `install.sh` / `systemd/migrate-agent-user.sh` が、`/opt/kizuna-eye` の**親ディレクトリのモード**を正規化していなかったため、root の umask 077 で作られた 0700 のまま非 root サービスユーザーが本体を exec できず `status=203/EXEC` で起動不能になる問題を修正（モードを明示的に `o+rx` へ）。systemd ユニット起動後に `is-active` を確認し、落ちていれば journal の見方と復旧コマンドを案内するようにした
- `kizuna-dashboard.service` の `StartLimitIntervalSec` / `StartLimitBurst` が `[Service]` にあり systemd に無視されていたため `[Unit]` へ移動（再起動の暴走制限が効いていなかった）
- 配備固有の絶対パス（`/samba/share/...`）を除去。プラグイン設定の既定値は作業ディレクトリ基準の相対パス、agent ユニットの `ReadWritePaths` は `__DIR__/logs`（`-` 付きで不在でも起動可）へ変更
- `install.sh` / `update.sh` / `scripts/build-plugin.sh` の外部プラグイン（Kizuna-Backup LITE）ソース位置を `KIZUNA_LITE_PLUGIN_DIR` による opt-in に統一（他のリポジトリを前提にしたパスを持たない）
- 移行済み環境（`ALREADY_INSTALLED`）では `migrate-agent-user.sh` をスキップするため agent unit が再生成されず、テンプレート修正が既存環境へ伝わらなかった問題を修正。`install.sh` が起動前に最新テンプレートで unit を再生成（旧版は `/tmp` に退避、未置換プレースホルダが残る場合は更新を中止）するようにした
- `kizuna-watchdog.service`（systemd user unit, Type=oneshot）が service 終了時に cgroup 内の agent を道連れに SIGTERM で殺し、agent が約2秒で停止を繰り返す問題を修正（`KillMode=none` を追加）
- `install.sh` / `update.sh` のビルドが Ubuntu 26.04 以降で失敗する問題を修正。先頭で `. /etc/os-release` を読むため `VERSION` が OS バージョン文字列（例 `26.04.1 LTS ...`）で上書きされ、ldflags に空白入りの不正値が渡っていた。os-release はサブシェルで必要値のみ取得し、ビルド用の変数を `KVERSION` に分離
- `install.sh` を root 実行したとき cron 読み取りヘルパー / ワンクリック対処ヘルパーの導入が誤ってスキップされる問題を修正（`[ -z "$SUDO" ]` は root で真になるため、`id -u -ne 0` を併せて判定）
- `update.sh` のプラグイン再署名が sudo 実行時に `/root/.kizuna-eye/...` の鍵を探して失敗する問題を修正（`SUDO_USER` のホーム基準で鍵を解決）
- `update.sh` / `safe_update.sh` が systemd 管理下で agent しか再起動せず、dashboard が古いバイナリのまま動き続ける問題を修正（dashboard + agent の両 unit を検出して再起動）
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
- 認証有効時に `style.css` と `auth-theme.js` が公開パスに無く、ログイン / セットアップ画面で読めずテーマが正しく表示されない問題（公開パスに追加）
- 設定エディタ（GUI）で通知チャンネルを開くと、Discord 以外のチャンネル（Slack / Telegram / LINE / Email）の `type` を `discord` に上書きしてしまい、保存すると他チャンネル設定が失われる問題（GUI が編集できるのは Discord のみとし、他種別は変更せず保持するよう修正）
- Email 通知の `smtp.SendMail` が context/タイムアウトを尊重せず、SMTP サーバー無応答時に goroutine がハングして通知セマフォを枯渇させる問題（`net.Dialer.DialContext` + `smtp.NewClient` に置換し、STARTTLS を維持しつつ deadline を適用）
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

### Security
- `auth.public_viewer` を追加。有効時は未ログインでもダッシュボード・`/ws`・`/api/status` のみ閲覧可能（履歴・アラート・ログ・管理系はログイン必須）
- エージェント起動時に rsync / smartctl の有無を確認し、ログと標準エラーに警告
- エージェントから受信したステータスを範囲検証（`CPUUsage` / `MemPercent` / `DiskPercent` が 0〜100 の範囲外なら破棄）。不正値がメトリクス履歴・`/api/metrics` に流れ込むのを防止
- プラグインアップロードをトランザクション化。検査失敗・`meta.json` 書き込み失敗・`modules.json` 登録失敗のいずれでも孤児 `.so` / `meta.json` を残さない
- プラグイン削除で `.so` の削除に失敗した場合は 500 を返す（実行可能な孤児 `.so` を残したまま「削除成功」を返さない）
- `plugin-inspect` の反射呼び出しにシグネチャ検証と recover を追加（悪意ある `.so` による panic を防止）
- 設定エディタの秘密復元を位置ではなく `type` で対応付け（通知チャンネルの並べ替え・削除で別チャンネルの秘密を誤割り当てしない）
- プラグイン `.so` の Ed25519 **分離署名検証**を追加（`internal/pluginsig` / `cmd/plugin-sign`）。Go プラグインは `plugin.Open` で `init()` が走るため、署名の無い/一致しない `.so` は**読み込み前**に拒否する（`plugins.require_signature=true` で fail-closed）
- プラグイン検査（`plugin-inspect`）を bubblewrap で隔離実行（`--unshare-all --die-with-parent --ro-bind / / --tmpfs /tmp`）。`plugins.inspect_isolation` が `bwrap`（既定）で bwrap が無い場合は実行せずエラー（fail-closed）
- アップロードされた `.so` は `plugins_dir/.quarantine`（0700）へ一時保管し、検証成功後に同一 FS 内 `rename` で配置。署名検証 → 隔離検査 → 配置の順に固定
- プラグイン削除時に `.sig` も削除（同名 `.so` の再アップロードが過去の署名を継承しない）
- agent を専用システムユーザーで動かす systemd unit 一式と移行スクリプトを追加（A-4。当時のユーザー名は `kizuna-agent`、のちに `kizuna-eye` へ改名。unit は `kizuna-eye-agent.service` / `kizuna-dashboard.service` / `kizuna-watchdog.{service,timer}` / `install-services.sh` / `migrate-agent-user.sh` / `setup-coredump.sh` / `60-kizuna-core.conf`）
- A-4 移行後も agent が共有設定（`agent_config.json` / `modules.json`）を読めるよう `fsutil.TightenSharedConfigMode` を追加。0600 へ締めつつ、移行が与えた group read（0640）は保持する（0644 / 0666 / 0777 は 0640、それ以外は 0600）。ダッシュボードが保存するたびに 0600 へ戻して agent が読めなくなる問題を防止
- アラート履歴 `logs/alert_history.jsonl` も同じ共有契約の対象にした（`fsutil.SharedFileMode`）。ダッシュボードが起動時（`SetPersistence`）・追記（`O_CREATE`）・圧縮（`WriteFileAtomic`）のいずれでも 0600 へ戻さず、A-4 移行が与えた 0640（`Clear` 後の再作成は共有ディレクトリなら 0640）を保つ。agent（のちに `kizuna-eye` へ改名）が履歴を読めず V2-B の整合性検証が `alert_history_tamper` を誤報し続ける問題を防止
- SUID/SGID 検知に「高リスク領域の常時走査」を追加（`suid_fast_check` / `suid_fast_paths` / `suid_fast_baseline_path` / `suid_fast_interval_sec`）。従来は `suid_paths`（既定は `/usr` 以下のみ）を `suid_scan_hour`（既定3時）に1時間間隔で1回だけ走査していたため、`/tmp` 等に SUID バイナリを置かれても一度も検知できなかった。既定で `/tmp`, `/var/tmp`, `/dev/shm`, `/run`, `/home`, `/opt`, `/srv` を時間帯制限なし（`scanHour=-1`）・10秒間隔で走査し、新規 SUID/SGID ファイルは critical、既知ファイルのモード変更は warning として通知する（ベースラインは `./logs/kizuna-security-suid-fast.json` に分離）
- SUID/SGID 検知を **inotify によるイベント駆動**にした（`suid_watch` / `suid_watch_paths`）。周期走査だけでは「走査と走査の間に作成〜削除が完結したファイル」を原理的に見られず、実機の攻撃テストで 7 回中 2 回取りこぼしていた（エージェントのポーリング 15 秒に対し存在窓 10 秒）。既定で `/tmp`, `/var/tmp`, `/dev/shm`, `/run` を監視し、ディレクトリの変化（作成・改名・属性変更）を契機に即時走査するため、検知は通常 1 秒未満になる。監視は深さ 3 階層・最大 2048 本に制限し、`fs.inotify.max_user_watches`（ホスト全体の予算）を食い潰さない。使えない環境では周期走査のみへ自動的に戻る
- 高リスク領域の SUID 走査をエージェントのポーリング間隔から切り離した（専用ループ + `SUIDMonitor.CheckForce`）。ポーリング間隔で量子化されると `suid_fast_interval_sec=10` が実効 15 秒になり、短命なファイルを取りこぼす。下限は 5 秒（5 未満は既定 10 秒へ）
- SSH 失敗ログインの検知を強化した（`failed_sustained_burst` / `failed_sustained_window_sec` / `ssh_enum_distinct_users` / `ssh_enum_window_sec`）。実機の攻撃テスト（攻撃 4）で、失敗回数を「最初の失敗から `burst_window_sec` 以内」で数えていたため、20〜80 秒間隔でユーザー名を回す試行が全件 warning 止まりとなり critical（およびブロック候補）に到達しなかった。直近 `burst_window_sec` のスライディング窓へ改め、加えて低頻度・長時間の総当たり（既定 15 回 / 600 秒）とユーザー名の列挙（既定 5 種類 / 300 秒）を別規則の critical とした。エスカレーションは 1 送信元につき 1 回で、長窓ぶん静穏になると再武装する
- ループバック (127.0.0.1) からの失敗ログインに、送信元 IP では攻撃元を特定できない旨の注記を追加（ホスト上に侵入済みの攻撃者、または 127.0.0.1 へのトンネル経由。実機の攻撃 4 はこの形だった）
- 「SSH ログイン成功の急増」の再通知を IP ごとにレート制限（冷却 = `ssh_login_window_sec` の経過後、かつ回数が前回通知の 2 倍以上）。既知 IP からのものには注記を付けた。監視ツール・運用端末の反復接続が窓ごとに同じ critical を出し、本物の失敗の signal を埋める問題（監査 F-6）を解消
- ログ由来のユーザー名を通知に載せる前に制御文字を除去し 64 文字へ切り詰める（攻撃者が自由に決められる値による通知の偽装・肥大化を防止）
- FIM に**ディレクトリ監視（inotify）**を追加（`fim_watch` / `fim_watch_paths` / `fim_watch_interval_sec` / `fim_watch_max_files` / `fim_watch_max_size_kb` / `fim_watch_ignore` / `fim_watch_baseline_path`）。`integrity_files` は事前に列挙した絶対パスしかハッシュしないため、攻撃者が任意の名前でファイルを作る置き場（`/tmp` 等）は原理的に検知できない。実機の攻撃 A-5（`/tmp/kizuna-fim-test/watched.txt` の作成→変更→削除）は、FIM の走査が対象の存在中に 2 回入っていたにもかかわらず 1 件も検知されなかった。inotify が通知したパスだけをその場でハッシュして差分を検知し（作成・変更は critical、削除と「走査前に消えた短命なファイル」は warning）、専用ループ（既定 10 秒）の周期走査は inotify が使えない環境・取りこぼしのフォールバックとして回す。走査は深さ 3 階層・最大 `fim_watch_max_files` 件（既定 4096）・`fim_watch_max_size_kb` 以下（既定 4MiB）に制限し、走査できない範囲がある場合は状態が変わったときだけ警告する（ビジーなディレクトリで履歴が埋まるのを防ぐ）。ベースラインは FIM 本体と同じ鍵で署名する（ファイルを書き換えて「改ざん無し」の初期状態に戻せない）。既定は `fim_watch=false`（`/tmp` 全体を丸ごと監視すると正規の一時ファイルでも通知が出るため、対象を絞るか `fim_watch_ignore` と併用して有効化する）
- inotify 層を汎用化（`newInotifyWatcher`）。従来は「何か変わった」しか通知できず SUID 走査の起動にしか使えなかったが、変化したパスの一覧も渡せるようにした（FIM ディレクトリ監視がそのパスだけを走査するために必要）。ログの接頭辞は機能名（`SUID` / `FIM`）で出し分ける
- FIM ディレクトリ監視を実機の攻撃 A-5（`/tmp/kizuna-fim-test` の作成→変更→削除）で検証して見つかった 2 点を修正。(1) 監視ルートがまだ存在しない場合は最も近い既存の親を監視し、ルートが作られた時点で本来の監視を張る（配備直後に `/tmp/kizuna-fim-test` が無く「0 個のディレクトリを監視」となり、ディレクトリ作成から始まる最初の攻撃を取りこぼした）。(2) inotify の `IN_ISDIR` を区別し、ディレクトリを「作成直後に消えた短命なファイル」として誤報しない（実機で誤報を確認）。代わりにディレクトリが変化した場合は中を走査する（`mkdir` 直後にファイルを置かれると、inotify の監視設置前に作成が済んでいて作成イベントを取りこぼすため）
- FIM ディレクトリ監視を実機の攻撃テスト（`/tmp/kizuna-fim-test` の変種1〜4）で再検証して見つかった4件を修正。(1) symlink のリンク先を記録しておらず、通常ファイル→symlink の差し替えやリンク先の変更（内容偽装）を検知できなかった → `kind=symlink` とリンク先をベースラインに持ち、どちらの差し替えも critical にした。(2) `fim_watch_max_size_kb` を超えるファイルはベースラインに載らず、逆に「走査する前に削除されました」と誤報していた → 内容ハッシュの代わりにサイズ・mtime・inode を記録し（`kind=large`）、作成・変更・削除を warning で検知する。(3) inotify 経路が「ファイル自身の深さ」で落としていたため、上限ちょうどの階層は周期走査が入るまで無言だった → 判定を「パスを収めるディレクトリ」基準に直し、上限を超えたパスは 1 パス 1 回 warning にした（深さ上限を `fim_watch_max_depth` / 既定 3、0 = ルート直下のみ として設定可能にした）。(4) inotify の監視ディレクトリ数上限（ルートごと）に達しても無言で即時検知が止まっていた → 上限を `fim_watch_max_dirs`（既定 1024）で設定可能にし、到達を 1 ルート 1 回 warning にした（SUID 監視と共通の inotify 層に実装したため両方に効く）
- FIM の「監視ディレクトリの一部を走査できません」判定を全体走査のときだけ行うようにした。イベント走査（上限や深さで落ちたパスしか見ていない）の結果で状態を戻していたため、周期走査のたびに 16 件 → 0 件 → 16 件と振動し、変化のたびに同じ warning が出ていた
- FIM の差分検知に inode を追加した（`fim_watch_inode_linux.go` / `_other.go`）。同一パス・同一サイズ・同一 mtime（秒精度）で置き換えられても inode の変化で検知できる（非 Linux は size+mtime に縮退）
- `fim_watch_ignore` のパターンを、除外ディレクトリの配下のファイルにも適用するようにした。`systemd-private-*` のようなディレクトリ名指定が中のファイルまで届かず、`/tmp` の監視が正当な一時ファイルで埋まっていた
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