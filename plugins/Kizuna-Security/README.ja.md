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
| failed_burst | SSH 失敗を「多発」とみなす回数 | 5 |
| burst_window_sec | 失敗回数を数える時間枠（秒） | 60 |
| log_path | 検知ログの保存先 | ./logs/kizuna-security.log |

RHEL 系では watch_files に /var/log/secure や /var/log/messages、yum/dnf ログも指定できます。

### SSH ログイン成功の監視

| キー | 説明 | 既定値 |
|---|---|---|
| ssh_login_burst | 同一 IP のログイン成功を「急増」とみなす回数 | 10 |
| ssh_login_window_sec | 成功回数を数える時間枠（秒） | 300 |
| ssh_login_baseline_path | 既知 IP の保存先（未知 IP 判定用） | ./logs/kizuna-security-logins.json |

### 追加の検知

| キー | 説明 | 既定値 |
|---|---|---|
| listen_port_check | 新規リッスンポートを検知する | true |
| suid_check | SUID/SGID ファイルを検知する | true |
| suid_paths | 走査するディレクトリ | /usr/bin,/usr/sbin,/bin,/sbin,/usr/local/bin,/usr/local/sbin |
| suid_scan_interval_sec | SUID/SGID スキャン間隔（秒） | 3600 |
| suid_scan_hour | 走査する時刻（0-23時、-1で制限なし） | 3 |
| cron_check | cron の変更を検知する | true |
| cron_paths | cron 監視パス | /etc/crontab,/etc/cron.d,/var/spool/cron など |

SUID/SGID 走査は指定した時刻台にのみ実行し、nice/ionice が利用可能なら低優先度で走ります（非力なサーバーへの配慮）。

### ファイル完全性監視（FIM）

| キー | 説明 | 既定値 |
|---|---|---|
| integrity_files | 改ざん監視する重要ファイル | /etc/passwd など |
| integrity_baseline_path | ベースラインの保存先 | ./logs/kizuna-security-fim.json |

監視対象に後から追加したファイルは、黙って取り込まず**必ず通知**します（攻撃者が新しいバックドアをベースラインへ紛れ込ませる抜け道を塞ぐため）。意図した追加かどうかを通知で確認してください。

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
- ブロックの dry-run / enforce / ホワイトリスト分岐、状態の復元と期限切れ破棄
- ハッシュチェーンの改ざん・削除・挿入の3パターン検出
- 通知メッセージの i18n（ja / en とフォールバック）
- SUID 走査の時間帯制限と新規検出
