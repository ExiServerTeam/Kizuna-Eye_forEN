# Kizuna-Eye 操作手順書

本書は、Kizuna-Eye の導入・設定・運用・プラグイン開発・検証の手順をまとめたものである。
対象は Ubuntu Server 20.04 以降の Linux 環境とする。

---

## 1. 導入

### 1.1 動作要件

- Go 1.27.1 以上
- CGO が有効であること（`plugin` パッケージのため）
- プラグインをビルドする場合は gcc（`build-essential`）
- SSH ファイル転送を使う場合は rsync
- ディスクの S.M.A.R.T を使う場合は smartctl（任意）

### 1.2 取得とビルド

次を実行する。

    git clone https://github.com/sy815twty-spec/Kizuna-Eye.git
    cd Kizuna-Eye
    go mod download
    ./build.sh

`build.sh` は次のバイナリを `/opt/kizuna-eye/bin/` に出力する。

- `agent_linux`
- `dashboard_linux`
- `plugin-inspect`
- `plugin-sign`（プラグイン `.so` の Ed25519 署名ツール）

### 1.3 設定ファイルの準備

    cp dashboard_config.example.json dashboard_config.json
    cp agent_config.example.json agent_config.json
    cp modules.json.example modules.json

各ファイルを環境に合わせて編集する。秘密情報を含むため、権限は 0600 に保つこと。

### 1.4 起動

    ./start.sh

ブラウザで `http://<サーバーのIP>:8080` を開く。

一部だけを起動したい場合は `./start.sh dashboard` / `./start.sh agent` を使う
（停止は `./stop.sh dashboard` / `./stop.sh agent`）。引数省略時は従来どおり両方を
起動・停止する。

#### agent を専用ユーザーで動かす場合（A-4）

`systemd/migrate-agent-user.sh` で agent を専用ユーザー `kizuna-agent` へ移行すると、
以降 agent は systemd（`kizuna-agent.service`）が管理する。このとき `start.sh` /
`stop.sh` は **agent を操作しない**（agent は別ユーザー所有なので kill できず、
systemd 管理下の agent と二重に動くと状態ファイルを奪い合うため）。agent の操作は
systemctl を使う。

    systemctl status kizuna-agent
    sudo systemctl restart kizuna-agent
    journalctl -u kizuna-agent -f

dashboard は従来どおり手動管理なので、移行後は次で再起動する。

    ./stop.sh dashboard
    ./start.sh dashboard

agent を強制的に手動管理へ戻す場合は `KIZUNA_FORCE_MANUAL=1 ./start.sh` を使う
（systemd 側は `systemctl disable --now kizuna-agent` で止めておくこと）。
移行の設計と適用手順は `docs/session_hardening_a1_a4_20261003.md` を参照。

移行後は agent がアラート履歴 `logs/alert_history.jsonl` を整合性検証（V2-B）で読む
ため、ファイルにはグループ `kizuna-eye` の読み取り（0640）を与える。ダッシュボードは
保存・追記・圧縮のどの経路でもこの group read を保つ（`fsutil.SharedFileMode`）ので、
再起動しても agent は読み続けられる（0600 へ戻すと `alert_history_tamper` を誤報する）。

---

## 2. 設定

### 2.1 ダッシュボード

`dashboard_config.json` の主な項目は次のとおりである。

- `listen_addr`: 待ち受けアドレス（例 `:8080`）
- `static_dir`: 画面ファイルの場所
- `plugins_dir`: プラグインの配置先
- `plugins_upload_enabled`: 画面からのプラグインアップロードの可否（既定は false）
- `alert_history_file`: アラート履歴の保存先
- `auth`: 認証設定（後述）
- `notifications`: 通知設定（後述）

### 2.2 エージェント

`agent_config.json` の主な項目は次のとおりである。

- `dashboard_url`: 接続先の WebSocket URL
- `interval`: メトリクスの送信間隔（秒）
- `log_file`: ログの保存先
- `disk_path`: 監視するディスクのパス
- `token`: ダッシュボードと共有する Agent トークン

---

## 3. 認証

認証は既定で無効である。有効にするには `dashboard_config.json` の `auth` ブロックで `enabled` を `true` にする。

有効にすると、初回アクセス時に `/setup.html` へ移動し、最初のユーザーを作成する。最初に作成したユーザーが管理者となる。

### 3.1 権限

| 権限 | できること |
|---|---|
| viewer | ダッシュボードとログの閲覧 |
| operator | 上記に加え、モジュール管理・手動バックアップ実行・アラート閾値の変更 |
| admin | 全権（設定エディタ・プラグインのアップロード・ユーザー管理） |

### 3.2 Agent トークン

`dashboard_config.json` の `auth.agent_token` と `agent_config.json` の `token` に同じ値を設定する。Agent は接続時にこれを提示する。空の場合は従来の判定方式となる。

### 3.3 HTTPS

認証情報を平文の HTTP で送信してはならない。リバースプロキシで TLS を終端することを推奨する。

1. ダッシュボードを localhost にバインドする（`listen_addr` を `127.0.0.1:8080` にする）。
2. Caddy や nginx で TLS を終端し、`127.0.0.1:8080` へプロキシする。
3. `auth.secure_cookies` を `true` にする。

---

## 4. 通知

`notifications.channels` は `discord`、`slack`、`telegram`、`line`、`email` に対応する。各タイプに必要な項目は `dashboard_config.example.json` を参照すること。

アラート閾値は、設定エディタ（admin）から再起動なしで変更できる。

---

## 5. プラグイン

### 5.1 開発

プラグインは Go 標準の `plugin` パッケージを用いて `.so` としてビルドする。実装すべき主なインターフェースは次のとおりである。

- `Module`: `Name`、`Description`、`Interval`、`Run`、`Init`
- `ConfigProvider`: `GetConfigFields`（任意）
- `DisplayNameProvider`: `DisplayName`（任意）

ビルドは次のとおりである。

    CGO_ENABLED=1 go build -buildmode=plugin -o my_plugin.so .

### 5.2 配備

Web ダッシュボードの「モジュール管理」タブから `.so` をアップロードする。組み込みの `plugin-inspect` がプラグインを解析し、設定項目を抽出して動的な設定フォームを生成する。

署名必須（`plugins.require_signature: true`、既定）の場合は、事前に `plugin-sign -sign my_plugin.so -private-key <秘密鍵>` で `my_plugin.so.sig` を作り、`.so` と同時にアップロードする。署名検証は `plugin-inspect`（＝`plugin.Open` による `init()` 実行）より**前**に行われ、署名が無い/一致しない `.so` は拒否される。

### 5.3 再ビルドが必要な場合

`pkg/module` または `pkg/status` の公開型を変更した場合は、Agent 本体と全プラグインを同一ソースから同時に再ビルドする必要がある。片方だけを再ビルドすると、読み込み時に「異なるバージョンのパッケージ」として失敗する。

---

## 6. 検証

### 6.1 開発環境での検証

    go test ./...

gcc のある Linux では、データ競合の検出も行う。

    CGO_ENABLED=1 go test -race ./...

静的解析を実行する場合は次のとおりである。

    go vet ./...
    staticcheck ./...
    govulncheck ./...

### 6.2 サーバーでの総合検証

`scripts/verify.sh` を使用する。

    ./scripts/verify.sh          # 静的検証（gofmt / vet / test / race / i18n）
    ./scripts/verify.sh --smoke  # 上記 + ビルド + 起動 + API/WS スモーク
    ./scripts/verify.sh --full   # --smoke + プラグイン .so の検査

gcc が無い環境では race 検証は自動的にスキップされる。Linux サーバーでの実行を推奨する。

### 6.3 手動確認

自動化できない領域は、ブラウザで次を確認する。

- ダッシュボードの指標が更新される（Agent が接続されている）
- メトリクス履歴が伸びる（約1時間分を保持）
- モジュール管理からプラグインを追加できる（`.so` のアップロード）
- 手動実行（▶）で結果が表示される
- ログ画面で Agent と Dashboard を切り替えられる
- 設定エディタでひな形の挿入と保存ができる
- テーマと言語を切り替えられる

---

## 7. 運用上の注意

- 設定ファイルには秘密情報が含まれる。リポジトリへコミットせず、権限を 0600 に保つこと。
- `plugins_upload_enabled` を `true` にする場合は、必ず認証を有効にすること。認証が無効な状態でアップロードを許可すると、ネットワーク上の誰でもプラグインを実行でき、実質的なリモートコード実行となる。
- プラグインの `.so` は信頼できるものだけを配置すること。プラグインはサーバー本体と同じ権限で動作する。
- コミット前に `git status` を確認し、秘密情報を含むファイルが含まれていないことを確認すること。

---

## 8. トラブルシューティング

- Agent が接続しない場合は、`auth.agent_token` と `token` が一致しているかを確認する。
- プラグインが読み込まれない場合は、Agent 本体と同一ソースで再ビルドされているかを確認する。
- ログが表示されない場合は、ログファイルのパスと権限を確認する。
- `smartctl` が無い環境ではディスクの温度と健康状態は取得されない。
