# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.0] - 2026-10-05

### Added
- ログイン画面に「ゲストとしてログイン」ボタンを追加。`auth.public_viewer` 有効時のみ表示され、viewer 権限（読み取り専用）でダッシュボードに入れる
- ダッシュボードのカード（CPU/メモリ/ストレージ/稼働時間）をドラッグで並べ替え可能に（順序は localStorage に保存）
- ストレージカードをクリックすると CrystalDiskInfo 風の詳細モーダルを表示（健康状態・温度・型番・シリアル・総書込量・通電時間・使用率）
- ディスク S.M.A.R.T 情報を拡張（`smartctl` から型番・書込量・通電時間・回転数を取得。ATA/NVMe 両対応）
- WebSocket 切断時に「切断されました」ポップアップ、再接続時に「再接続しました」を表示
- 自動更新チェック（`internal/updater`）。GitHub Releases を監視し、新バージョン検知時に `safe_update.sh` を実行
- `install.sh` / `update.sh` / `safe_update.sh` を追加。必要なパッケージ（rsync / smartmontools / git / curl）を差分で確認・導入し、更新失敗時はロールバック

### Changed
- バージョン表記を v0.7.0 に統一（web 全体・Makefile・build.sh）
- アカウントメニューの絵文字を削除（アイコン変更 / パスワード変更 / ユーザー管理）
- CPUカードはCPU温度のみ、メモリカードは空き容量のみ、ストレージカードは温度＋空き容量のみを表示

### Security
- `auth.public_viewer` を追加。有効時は未ログインでもダッシュボード・`/ws`・`/api/status` のみ閲覧可能（履歴・アラート・ログ・管理系はログイン必須）
- エージェント起動時に rsync / smartctl の有無を確認し、ログと標準エラーに警告

### Added
- `uninstall.sh`: `install.sh` が導入した systemd サービス・unit・sudoers・ヘルパー・/etc/kizuna-eye・sysctl 設定を撤去する。`--purge` でデータ・鍵・バイナリ・専用ユーザーまで完全削除、`--dry-run` で内容確認のみ
- `install.sh` にプラグイン署名ステップを追加（純正プラグインのみ。署名鍵が無ければ生成し `plugin-sign -sign-all`）
- `install.sh` に systemd 登録（dashboard + agent）を内蔵。`--no-systemd` で手動管理に切替可
- `update.sh` にプラグイン再署名ステップを追加（再ビルド後に `plugin-sign -sign-all`）

### Removed
- 内部記録（作業記録・計画書・プラグイン監査記録）を `Kizunaシリーズ　資料/` へ退避し、公開リポジトリから削除。`docs/` は `PROCEDURE.md` / `ROADMAP.md` のみに整理
- 作業用スクリプト `.py` を全削除（`scripts/archive/patch_*.py`, `scan_emoji.py`, `ssh_run.py`, `test_ssh_fail.py`）

### Changed
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

### Fixed
- `kizuna-watchdog.service`（systemd user unit, Type=oneshot）が service 終了時に cgroup 内の agent を道連れに SIGTERM で殺し、agent が約2秒で停止を繰り返す問題を修正（`KillMode=none` を追加）
- `install.sh` / `update.sh` のビルドが Ubuntu 26.04 以降で失敗する問題を修正。先頭で `. /etc/os-release` を読むため `VERSION` が OS バージョン文字列（例 `26.04.1 LTS ...`）で上書きされ、ldflags に空白入りの不正値が渡っていた。os-release はサブシェルで必要値のみ取得し、ビルド用の変数を `KVERSION` に分離
- `install.sh` を root 実行したとき cron 読み取りヘルパー / ワンクリック対処ヘルパーの導入が誤ってスキップされる問題を修正（`[ -z "$SUDO" ]` は root で真になるため、`id -u -ne 0` を併せて判定）
- `update.sh` のプラグイン再署名が sudo 実行時に `/root/.kizuna-eye/...` の鍵を探して失敗する問題を修正（`SUDO_USER` のホーム基準で鍵を解決）
- `update.sh` / `safe_update.sh` が systemd 管理下で agent しか再起動せず、dashboard が古いバイナリのまま動き続ける問題を修正（dashboard + agent の両 unit を検出して再起動）

### Security
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
- `start.sh` / `stop.sh` に操作対象の引数（`all` / `dashboard` / `agent`、省略時は従来どおり両方）を追加。agent が systemd 管理下（`kizuna-eye-agent` / 旧 `kizuna-agent` が active または enabled）のときは agent を起動・停止しない（A-4 移行後の二重起動防止。強制する場合は `KIZUNA_FORCE_MANUAL=1`）
- `stop.sh` が kill の完了を確認せず「停止しました」と表示していた問題を修正。停止できなかった場合は警告を出して終了コード 1 を返す（別ユーザー所有のプロセスは EPERM で kill できないため）

### Added
- `cmd/plugin-sign`: Ed25519 鍵対生成 / `.so` への分離署名 / 検証 / 一括署名（`-gen-key` `-sign` `-sign-all` `-verify` `-print-public-key`）。`build.sh`・`install.sh`・`update.sh`・`safe_update.sh`・`scripts/build.sh`・`scripts/verify.sh` のビルド/ロールバック対象へ組み込み
- `install.sh` の必須パッケージに `bubblewrap` を追加（A-3 の隔離検査で使用。無い場合は検査が fail-closed で失敗するため）
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