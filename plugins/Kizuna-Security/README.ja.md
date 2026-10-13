# Kizuna-Security

Kizuna-Eye 用のセキュリティ監視プラグインです。SSH ログイン、sudo、パッケージのインストールなどのセキュリティに関わる操作をログから検知し、重要度の高いものは Discord とダッシュボードに通知します。

## 機能

| カテゴリ | 内容 | 既定レベル |
|---|---|---|
| ssh_login | SSH ログイン成功 | info |
| ssh_login（未知IP） | これまで見なかった IP からのログイン成功 | warning |
| ssh_login（急増） | 同一 IP からのログイン成功がしきい値に到達 | critical |
| ssh_failed | SSH ログイン失敗（不正ユーザー・認証中切断を含む） | warning |
| ssh_failed（多発） | 同一 IP からの失敗がしきい値に到達 | critical |
| sudo | sudo 実行 | warning |
| sudo（認証失敗） | sudo 認証失敗 | warning |
| account | アカウント操作（useradd / usermod / passwd 等） | warning |
| install | パッケージのインストール / アップグレード / 削除（dpkg / apt / yum / dnf） | warning |
| integrity | 重要ファイルの改ざん・作成・削除 | critical / warning |
| listen_port | 新規リッスンポートの検知 | warning |
| suid | 新規 SUID/SGID ファイル・モード変更の検知 | critical / warning |
| cron | cron ジョブの追加・変更・削除の検知 | critical / warning |
| block | 不審 IP のブロック（dry-run / enforce） | warning / critical |

- 検知したイベントは logs/kizuna-security.log に JSON Lines で記録します（ハッシュチェーン付き）。
- notify_min_level 以上のイベントだけを Discord とダッシュボードへ通知します（それ未満はログのみ）。
- 通知はダッシュボードのアラート履歴にも残ります。

### 通知メッセージの言語

通知は Agent 側（サーバー）で生成するため、ブラウザの UI 言語とは独立して `language`（`ja` / `en`）で切り替えます。既定は `ja` です。

## 構成

Kizuna-Security/plugin/ 配下:

- plugin.go : プラグイン本体（Module 実装）
- monitor.go : ログ監視・検知ロジック（SSH/sudo/install/SSH成功異常）
- portmon.go : 新規リッスンポート検知
- suid.go : SUID/SGID 検知（時間帯制限・低優先度実行）
- cronmon.go : cron 変更検知
- fim.go : ファイル完全性監視
- config.go : 設定のパースと検証
- messages.go : 通知メッセージの i18n カタログ（ja / en）
- logger.go : JSON Lines ロガー（ハッシュチェーン）
- blocker.go : IP 自動ブロック（iptables / nftables / firewalld、状態永続化）
- build.sh : ビルド & 配備スクリプト
- go.mod / go.sum

## ビルドと配備

Kizuna-Security/plugin に移動して build.sh を実行します。kizuna_security.so を生成し、/opt/kizuna-eye/bin/plugins/ にコピーします。

注意: このプラグインは Kizuna-Eye/pkg/module に依存しています。本体側で pkg/module の型を変更した場合は、必ず本体とプラグインを同じソースで再ビルドしてください。バージョンがずれると plugin was built with a different version of package ... で読み込みに失敗します。

## 登録

modules.json に Kizuna-Security/modules.example.json の内容を追加します。

## 設定項目

### 基本

| キー | 説明 | 既定値 |
|---|---|---|
| watch_files | 監視するログファイル（カンマ区切り） | auth.log, dpkg.log, apt/history.log |
| poll_interval_sec | ログを読み取りにいく間隔（秒） | 15 |
| language | 通知メッセージの言語（ja / en） | ja |
| notify_min_level | 通知する最小レベル（critical / warning / info） | warning |
| log_path | 検知ログの保存先 | ./logs/kizuna-security.log |

RHEL 系では watch_files に /var/log/secure や /var/log/messages、yum/dnf ログも指定できます。

### SSH ログイン成功の監視

| キー | 説明 | 既定値 |
|---|---|---|
| ssh_login_burst | 同一 IP のログイン成功を「急増」とみなす回数 | 10 |
| ssh_login_window_sec | 成功回数を数える時間枠（秒） | 300 |
| ssh_login_baseline_path | 既知 IP の保存先（未知 IP 判定用） | ./logs/kizuna-security-logins.json |

急増の通知は IP ごとにレート制限されます（冷却時間 = `ssh_login_window_sec` の経過後、かつ
回数が前回通知の 2 倍以上になったときだけ再通知）。監視ツールや運用端末からの反復接続でも
同じ急増が起きるため、通知が埋まって本物の失敗が見えなくなるのを防ぎます。

### SSH ログイン失敗の監視

| キー | 説明 | 既定値 |
|---|---|---|
| failed_burst | 短窓内の失敗を「多発」とみなす回数 | 5 |
| burst_window_sec | 失敗回数を数える短窓（秒）。直近この秒数のスライディング窓 | 60 |
| failed_sustained_burst | 長窓内の失敗を「持続的な総当たり」とみなす回数 | 15 |
| failed_sustained_window_sec | 失敗回数を数える長窓（秒） | 600 |
| ssh_enum_distinct_users | 同一送信元の異なるユーザー名を「列挙」とみなす種類数 | 5 |
| ssh_enum_window_sec | ユーザー名の種類を数える窓（秒） | 300 |

3 つの規則のいずれかに到達すると critical になります（1 送信元につき 1 回、長窓の
`failed_sustained_window_sec` だけ静穏になると再武装）。

1. 多発（`failed_burst` / `burst_window_sec`）— 短時間の burst
2. 持続（`failed_sustained_burst` / `failed_sustained_window_sec`）— 間隔を空けた低頻度の総当たり
3. 列挙（`ssh_enum_distinct_users` / `ssh_enum_window_sec`）— ユーザー名を回す試行

ループバック (127.0.0.1) からの失敗には、送信元 IP では攻撃元を特定できない旨が注記されます
（ローカルに侵入済みの攻撃者、または 127.0.0.1 へのトンネル経由の試行）。

### 追加の検知

| キー | 説明 | 既定値 |
|---|---|---|
| listen_port_check | 新規リッスンポートを検知する | true |
| suid_check | SUID/SGID ファイルを検知する | true |
| suid_paths | 走査するディレクトリ | /usr/bin,/usr/sbin,/bin,/sbin,/usr/local/bin,/usr/local/sbin |
| suid_scan_interval_sec | SUID/SGID スキャン間隔（秒） | 3600 |
| suid_scan_hour | 走査する時刻（0-23時、-1で制限なし） | 3 |
| suid_fast_check | 高リスク領域(/tmp 等)の SUID/SGID を常時検知する | true |
| suid_fast_paths | 高リスク領域の走査パス | /tmp,/var/tmp,/dev/shm,/run,/home,/opt,/srv |
| suid_fast_interval_sec | 高リスク領域の走査間隔（秒） | 10 |
| suid_watch | 高リスク領域を inotify で即時監視する | true |
| suid_watch_paths | inotify で即時監視するパス | /tmp,/var/tmp,/dev/shm,/run |
| cron_check | cron の変更を検知する | true |
| cron_paths | cron 監視パス | /etc/crontab,/etc/cron.d,/var/spool/cron など |

SUID/SGID 走査は指定した時刻台にのみ実行し、nice/ionice が利用可能なら低優先度で走ります（非力なサーバーへの配慮）。

一方、攻撃者が実際に SUID バイナリを置く場所（`/tmp`・`/var/tmp`・`/dev/shm`・`/home` 等）は
`suid_fast_paths` として時刻制限なし・既定 10 秒間隔で走査します。`/usr` 以下のような
変更の少ないツリーを毎回走査すると重いため、監視対象を 2 系統に分けています。

この走査はエージェントのポーリング間隔（既定 15 秒）に縛られない専用のループで動きます。
ポーリング間隔に量子化されると、10 秒の設定が実効 15 秒になり、存在窓の短いファイルを
取りこぼすためです（実測: ポーリング 15 秒・存在窓 10 秒で 7 回中 2 回の取りこぼし）。

さらに `suid_watch`（既定有効）は `/tmp` 等を inotify で監視し、ディレクトリの変化
（作成・改名・属性変更）を契機にその場で走査します。これは「走査と走査の合間に
作成〜削除が完結したファイル」を拾うための経路で、周期走査では原理的に見られません
（inotify 経路では通常 1 秒未満、周期走査でもフォールバックとして数秒以内に検知）。
inotify が使えない環境や fd が枯渇した場合は、自動的に周期走査のみへ戻ります。

inotify の監視対象は `suid_watch_paths` に限定しています。`fs.inotify.max_user_watches`
はホスト全体（systemd やエディタと共有）の予算なので、`/home` のような巨大なツリーを
丸ごと監視すると他のソフトウェアを巻き込みます。監視本数の上限は 2048 本・深さは 3 階層で、
あふれた分は周期走査が担当します。

### ファイル完全性監視（FIM）

| キー | 説明 | 既定値 |
|---|---|---|
| integrity_files | 改ざん監視する重要ファイル | /etc/passwd など |
| integrity_baseline_path | ベースラインの保存先 | ./logs/kizuna-security-fim.json |
| fim_watch | 監視ディレクトリを inotify で即時監視する | false |
| fim_watch_paths | 監視するディレクトリ | /tmp,/var/tmp,/dev/shm |
| fim_watch_interval_sec | 再走査（フォールバック）間隔（秒） | 10 |
| fim_watch_max_files | 1回の走査でハッシュする最大ファイル数 | 4096 |
| fim_watch_max_size_kb | ハッシュする最大ファイルサイズ（KB） | 4096 |
| fim_watch_ignore | 監視ディレクトリで除外する glob | (空) |
| fim_watch_baseline_path | ディレクトリ監視のベースライン保存先 | ./logs/kizuna-security-fim-dirs.json |
| fim_watch_max_depth | inotify で監視する深さの上限（0 = ルート直下のみ） | 3 |
| fim_watch_max_dirs | 1 ルートあたりに張る inotify 監視ディレクトリ数の上限 | 1024 |

監視対象に後から追加したファイルは、黙って取り込まず**必ず通知**します（攻撃者が新しいバックドアをベースラインへ紛れ込ませる抜け道を塞ぐため）。意図した追加かどうかを通知で確認してください。

`integrity_files` は**事前に列挙した絶対パス**しかハッシュしないため、攻撃者が任意の名前でファイルを作る置き場（`/tmp` など）は原理的に検知できません。実機の攻撃 A-5 では `/tmp/kizuna-fim-test/watched.txt` の作成→変更→削除が、FIM の走査が対象の存在中に 2 回入っていたにもかかわらず 1 件も検知されませんでした（検知漏れの原因は走査タイミングではなく監視の単位）。

`fim_watch` を有効にすると `fim_watch_paths` 配下を inotify で監視し、**通知されたパスだけをその場でハッシュ**して差分を検知します。作成・変更は critical、削除と「走査する前に消えた短命なファイル」は warning です。周期走査（専用ループ・既定 10 秒）は inotify が使えない環境や取りこぼしのフォールバックとして回ります。

`/tmp` 全体のようなビジーなディレクトリを丸ごと有効化すると、正規の一時ファイルでも通知が出ます。対象を必要なサブディレクトリに絞るか、`fim_watch_ignore`（例: `*.swp`）で除外してください。走査の負荷は深さ 3 階層・最大 `fim_watch_max_files` 件（既定 4096）・`fim_watch_max_size_kb` 以下（既定 4MiB）に制限し、走査できない範囲がある場合はその旨を 1 回だけ警告します。ベースラインは FIM 本体と同じ鍵で署名され、書き換えによる監視の無効化はできません。

監視対象がまだ存在しない場合（例: 攻撃者が最初に `/tmp/kizuna-fim-test` を作る）は、最も近い既存の親を監視しておき、作成された時点で本来の監視を張ります。ディレクトリの変化は `IN_ISDIR` で区別し、「作成直後に消えた短命なファイル」とは扱いません（代わりに中を走査するので、`mkdir` 直後に置かれたファイルも、inotify の監視設置前に作成が済んでいても検知できます）。

検知の精度は次のように作られています。内容をハッシュするのは `fim_watch_max_size_kb`（既定 4096 = 4MiB）以下の通常ファイルで、上限を超える大容量ファイルはサイズ・更新時刻・inode のメタ情報だけで監視します（作成・削除・メタ情報の変化は warning、内容そのものの改ざんは原理的に検知できません）。symlink はリンク先を記録し、通常ファイル→symlink への差し替えとリンク先の変更を critical にします。`fim_watch_max_depth`（既定 3 階層）より深いパスと、`fim_watch_max_dirs`（既定 1024 本／ルート）に収まらないディレクトリは即時監視の対象外ですが、黙って検知範囲が狭まることはありません（1 パス・1 ルートにつき 1 回 warning で報告します）。深さ上限を超えたパスは周期走査でも対象外なので、必要なパスは `integrity_files` に列挙するか `fim_watch_max_depth` を上げてください。

### 自動ブロック

| キー | 説明 | 既定値 |
|---|---|---|
| block_mode | off / dry-run / enforce | off |
| firewall_backend | auto / iptables / nftables / firewalld | auto |
| block_duration_sec | ブロックを解除するまでの秒数 | 600 |
| block_state_path | ブロック状態の保存先 | ./logs/kizuna-security-blocks.json |
| block_whitelist | 絶対にブロックしない IP / CIDR | (空) |

enforce では特権実行で以下を行います（auto は firewalld → nftables → iptables の順に判定）。

- iptables: `iptables -I INPUT -s <ip> -j DROP`
- nftables: 専用テーブル `inet kizuna` のセットに追加
- firewalld: `firewall-cmd --add-rich-rule='rule source address="<ip>" drop'`

ブロック状態は `block_state_path` に永続化します。プラグイン再起動時は保存済みのブロックを復元し（残り時間も維持）、カーネルに残ったファイアウォールルールとの不整合を防ぎます。期限切れのブロックは復元時に破棄されます。

## 必要な権限

- /var/log/auth.log（RHEL は /var/log/secure）の読み取りには adm グループへの所属が必要です（Ubuntu/Debian）。対象ユーザーを adm グループに追加し、エージェントを再起動してください。
- 自動ブロック（enforce）には、iptables / nft / firewall-cmd をパスワードなしで実行できる権限設定が必要です。

## テスト

Kizuna-Security/plugin で `GOWORK=off go test -v .` を実行します。以下を単体テストで固定しています。

- SSH 失敗の多発検知（critical は1回のみ）・重複排除
- 未知 IP / ログイン成功の急増検知
- FIM の改ざん・作成・削除、および新規監視対象の通知
- FIM ディレクトリ監視（inotify）の作成・変更・削除・短命なファイル検知、上限（`fim_watch_max_files`）と除外パターン、ベースライン署名の改ざん検知（攻撃 A-5 の回帰）
- ブロックの dry-run / enforce / ホワイトリスト分岐、状態の復元と期限切れ破棄
- ハッシュチェーンの改ざん・削除・挿入の3パターン検出
- 通知メッセージの i18n（ja / en とフォールバック）
- SUID 走査の時間帯制限と新規検出
