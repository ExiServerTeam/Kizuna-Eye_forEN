# Kizuna-Eye 操作手順書

本書は、Kizuna-Eye の導入・設定・運用・プラグイン開発・検証の手順をまとめたものである。
対象は Ubuntu Server 20.04 以降の Linux 環境とする。

---

## 1. 導入

### 1.1 動作要件

- Go 1.27.2 以上（`go.mod` の `toolchain` で固定。ホストとプラグインは同一ツールチェーンでビルドすること）
- CGO が有効であること（`plugin` パッケージのため）
- プラグインをビルドする場合は gcc（`build-essential`）
- SSH ファイル転送を使う場合は rsync
- ディスクの S.M.A.R.T を使う場合は smartctl（任意）

### 1.2 取得とビルド

次を実行する。

    git clone https://github.com/ExiServerTeam/Kizuna-Eye_forEN.git
    cd Kizuna-Eye_forEN
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

各ファイルを環境に合わせて編集する。秘密情報を含むため、権限は 0600 に保つこと。

`modules.json`（モジュール登録）は**作成しない**。モジュールは Web UI の「モジュール管理」から追加するもので、初回インストールでは存在しないのが正しい状態である（無ければ「モジュール無し」として扱われる）。example をコピーすると未設定のモジュールが有効になり、空ファイルを作っても UI に不要な項目が出る。

### 1.4 セットアップ（install.sh 一本）

初回セットアップは `install.sh` だけで完結する。次を実行する。

    sudo ./install.sh

`install.sh` は次を順に行う。

1. 必要パッケージの差分導入（smartmontools / rsync / git / curl / build-essential / bubblewrap）
2. Go の確認
3. 本体ビルド（`plugin-inspect` / `dashboard_linux` / `agent_linux` / `plugin-sign`）
4. プラグインのビルドと署名（純正プラグインのみ。署名鍵が無ければ生成）
5. sudoers の導入（smartctl / cron ヘルパー / ワンクリック対処）
6. **systemd 登録**（dashboard + agent）。agent は専用ユーザー `kizuna-eye` で
   動かす（H-1 権限分離）。`systemd/migrate-agent-user.sh` を内部で呼ぶ
7. 起動

主なオプション:

    ./install.sh --no-install    # パッケージ導入をスキップ
    ./install.sh --no-build      # ビルドをスキップ
    ./install.sh --no-start      # 起動をスキップ
    ./install.sh --no-systemd    # systemd 登録をスキップ（start.sh/stop.sh で手動管理）

完了後、ブラウザで `http://<サーバーのIP>:8080` を開く。

#### 手動管理（--no-systemd）で使う場合

`./install.sh --no-systemd` で導入した場合、dashboard / agent は `start.sh` /
`stop.sh` で管理する。

    ./start.sh                 # dashboard + agent
    ./start.sh dashboard       # dashboard のみ
    ./stop.sh                  # 両方停止

#### systemd 管理下の操作（既定）

既定では dashboard と agent は systemd が管理する。agent は専用ユーザー
`kizuna-eye`（`kizuna-eye-agent.service`）で動く。このとき `start.sh` / `stop.sh` は
**agent を操作しない**（agent は別ユーザー所有なので kill できず、systemd 管理下の
agent と二重に動くと状態ファイルを奪い合うため）。操作は systemctl を使う。

    systemctl status kizuna-eye-agent
    sudo systemctl restart kizuna-eye-agent
    journalctl -u kizuna-eye-agent -f

dashboard は `kizuna-dashboard.service` で動く。

    systemctl status kizuna-dashboard
    sudo systemctl restart kizuna-dashboard

agent を強制的に手動管理へ戻す場合は `KIZUNA_FORCE_MANUAL=1 ./start.sh` を使う
（systemd 側は `systemctl disable --now kizuna-eye-agent` で止めておくこと）。

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
| viewer | ダッシュボードの閲覧、メトリクス・履歴・アラート履歴・バージョンの閲覧（読み取りのみ） |
| operator | viewer に加え、ログ閲覧・**既存モジュールの設定変更（コード実行境界を除く）**・モジュールの手動実行・アラート閾値の変更・アラート履歴の閲覧 |
| admin | operator に加え、**コード実行境界に関わる操作**（モジュールの追加・削除・reload、プラグインのアップロード・削除、**plugin_path の変更**）、設定エディタ、ユーザー管理、ワンクリック対処、アラート履歴のクリア |

**operator と admin の境界は「コード実行境界を越えるか」で引く。**

- operator は**既存モジュールの設定値**（監視対象パス、閾値、間隔、除外リストなど）を変更できる。ただし **plugin_path（ロードする .so の指定）は変更できない**（admin のみ）。
- operator は**モジュールの追加・削除・再読み込み**はできない（admin のみ）。
- operator は**アラート履歴を閲覧**できるが、**クリア（削除）はできない**（admin のみ。監査証跡を消す操作のため）。

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
- `systemctl status` が `status=203/EXEC`（`Failed to execute`）で失敗する場合は、本体を実行するユーザーがバイナリとその**親ディレクトリ**を辿れるかを確認する。`/opt/kizuna-eye` が root の umask 077 で作られた 0700 のままだと、バイナリに実行権を付けても exec できない（`ls -ld /opt/kizuna-eye /opt/kizuna-eye/bin`）。復旧は次の 2 行（owner は実行ユーザー、グループは kizuna-eye）：

      sudo chmod o+rx /opt/kizuna-eye /opt/kizuna-eye/bin
      sudo chgrp kizuna-eye /opt/kizuna-eye/bin/agent_linux && sudo chmod 0750 /opt/kizuna-eye/bin/agent_linux

  `install.sh` / `systemd/migrate-agent-user.sh` はディレクトリのモードを明示的に正規化するようになったため、再実行で同じ状態には戻らない。

---

## 9. HMAC 鍵のローテーション

改ざん検知用の HMAC 鍵は「1鍵1用途」で分離している。用途ごとに独立してローテーションできる。

| 鍵 | 既定パス | 用途 | 書き手 / 読み手 |
|---|---|---|---|
| chain.key | /var/lib/kizuna-eye/keys/chain.key | セキュリティログのハッシュチェーン | agent / agent |
| alert_history.key | <data>/keys/alert_history.key | アラート履歴の行単位署名 | dashboard / dashboard + agent |

### 9.1 セキュリティログのチェーン鍵（chain.key）

ローテーションは「鍵を差し替えると、プラグインが既存チェーンの不一致を検出して旧ログを退避し、新鍵で新チェーンを開始する」動作を利用する。

1. 旧ログを保全する（証跡のため任意の場所へコピー）。
2. 新しい鍵に差し替える:

       sudo rm -f /var/lib/kizuna-eye/keys/chain.key
       sudo systemctl restart kizuna-eye-agent   # 起動時に 32B を再生成（0600）

3. agent ログで `chain_quarantined`（WARN）と、旧ログの退避（`kizuna-security.log.legacy-<ts>`、0600）を確認する。以降の検証は新チェーンで継続する。

   補足: 鍵を失った場合も同じ経路で復旧する（旧ログは「検証不能な旧ログ」として退避され、新チェーンが始まる）。

### 9.2 アラート履歴の署名鍵（alert_history.key）

1. 鍵を退避して差し替える:

       sudo mv <data>/keys/alert_history.key <data>/keys/alert_history.key.old-$(date +%Y%m%d)
       sudo systemctl restart kizuna-dashboard   # 起動時に 0640 で再生成

2. 以降に追記される行は新鍵で署名される。**既存行は旧鍵の署名のまま**なので、プラグインの行検証では「旧鍵で署名された行」を不一致として警告し得る。証跡を残したい場合は旧鍵を保管し、必要に応じて検証側で両鍵を試す運用とする（現状は単一鍵）。

   注意: 鍵は dashboard と agent が共有する（dashboard が書き、agent が検証する）。差し替え後は両方の再起動、またはグループ kizuna-eye の読み取り（0640）が維持されていることを確認する。

### 9.3 鍵のバックアップ

- 鍵は 0600/0640 のまま、アクセス制御された別ストレージ（Samba 共有外）へ退避する。
- Samba 共有（guest 許可）には置かない。漏洩は改ざん検知の無効化に直結する。
