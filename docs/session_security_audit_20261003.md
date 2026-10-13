# Kizuna-Security 脆弱性・検知精度監査（読み取り専用）

**セッション日付**: 2026年10月3日
**作業内容**: コード読解によるセキュリティホールの洗い出し（タスク1）、V1〜V5 の検証可能性の判定（タスク2）
**方針**: 攻撃は実施せず、攻撃スクリプトも作成しない（タスク3は辞退。理由は末尾）。

---

## 0. 監査の前提（最初に読むこと）

### 0-1. 今回読んだコード
- Kizuna-Eye 本体: `internal/auth/middleware.go`, `internal/auth/ratelimit.go`, `internal/api/plugins.go`, `pkg/alert/engine.go`, `pkg/alert/history.go`, `cmd/dashboard/main.go`(840-1020行)
- プラグイン: `_pentest_backup_20261003_005910/Kizuna-Security-plugin/logger.go`, `monitor.go`, `fim.go`
- 補助: `.gitignore`, `scripts/patch_fim_deadlock.py`

### 0-2. 重要な前提（この監査の限界）
1. **配備版プラグインの実体はリポジトリ外にある。**
   `scripts/patch_*.py` の `DEFAULT_PATH` は `/samba/share/Kizuna-Security/plugin/*.go`。リポジトリ内の
   `_pentest_backup_*/Kizuna-Security-plugin/` は**その複製スナップショット**であり、少なくとも V1/V2/V4 は未適用:
   - `logger.go` に `hmac` が一切なく、`chainHash` は**鍵なし SHA-256**（V1 前）
   - `NewFileLoggerKeyed` / `ChainKeyPath` が存在しない（`patch_v1v2.py` が追加する予定の関数）
   - `fim.go` に `langMu` が無い（`patch_fim_deadlock.py` 未適用 = F-1 のデッドロックが残存）
   - `monitor.go` に journald 照合が無い（V5 前）
2. したがって **V1〜V5 の「合否」はこのリポジトリのコードだけでは判定できない**。配備版の
   `/samba/share/Kizuna-Security/plugin/*.go`（および `/samba/share/Kizuna-Eye/*` の差分）が必要。
3. **セキュリティ最重要コードが版管理外**: `.gitignore` が `_pentest_backup_*/` と `plugins/` を除外するため、
   プラグインのソースは git に一切入っていない。V1〜V5 の差分レビュー・回帰確認が不可能な状態。
   → `plugins/Kizuna-Security/` として本体リポジトリに取り込み、コミットすることを強く推奨。

### 0-3. 良かった点（先に評価）
- プラグインのログ: `logger.go:35` は `O_NOFOLLOW`, 0600 でオープン。チェーン破損時は `.legacy-*` へ退避 (logger.go:42-55)。
- FIM: `hashFile` は `O_NOFOLLOW` + 通常ファイル限定 (fim.go:175-187)。ベースライン保存は `CreateTemp`+`Chmod(0600)`+`Rename` の原子的更新 (fim.go:226-260)。
- プラグイン web UI 配信: `sanitizeName` → `filepath.Clean` → `EvalSymlinks` でシンボリックリンク脱出まで対策 (plugins.go:144-212)。
- ダッシュボード: Agent トークン比較は `subtle.ConstantTimeCompare` (main.go:862)。WS は非 Agent のイベント/ステータスを破棄 (main.go:956-1000)。`SetReadLimit(8MiB)` (main.go:892)。
- 認証: bcrypt(DefaultCost) + ダミーハッシュによるタイミング均一化、ユーザー名列挙対策 (store.go:82-90, 297)。
- レート制限: `X-Forwarded-For` を loopback ピアのみ信頼 (ratelimit.go:56-84)。これは正しい判断。
- 監視側の上限: `maxQueueSize`/`maxFailTrackers`/`maxLoginTrackers`/`maxWatchFileSize` (monitor.go:19-25)。

---

## 1. タスク1: 検知エンジン（プラグイン）側のホール

対象パスは特記なき限り `_pentest_backup_20261003_005910/Kizuna-Security-plugin/`（= 配備版の推定元）。

### F-1 [critical] FIM が「改ざんを検知した瞬間」に自己デッドロックして停止する

- 該当: `fim.go:61-62`（`f.mu.Lock()` を保持）→ `fim.go:102,112,123,140` で `f.lang()` を呼ぶ →
  `fim.go:157-164` の `lang()` が同じ `f.mu` を再度 `Lock()`。Go の `sync.Mutex` は再入不可。
  `SetLang`（fim.go:167-171）も同じ。
- 攻撃シナリオ: 監視対象ファイルを1バイト書き換える（`echo x >> /etc/passwd` 等）。FIM の変更検知分岐
  (fim.go:119-128) に入った時点で goroutine が `f.mu` を握ったまま永久ブロック。以降 FIM は二度と動かず、
  `f.mu` を要求する `Check()` 呼び出し元（プラグインの run ループ）もろとも停止する。
  **攻撃者が最初にやるべき「検知を殺す1手」**がこれ。
- 深刻度: **critical**（検知停止 + DoS。証跡も残らない）
- 修正: `language` 専用の `langMu sync.RWMutex` を分離（`scripts/patch_fim_deadlock.py` がまさにこの修正）。
  ただし**このスナップショットには未適用**。配備版に適用済みか、明日の作業前に必ず確認する。
  確認コマンド例: `grep -n 'langMu' /samba/share/Kizuna-Security/plugin/fim.go`
- 再発防止: `go test -race` ではなく、`Check()` を1回呼んで**戻ってくること**を assert する単体テスト
  （`FIM.Check()` を goroutine + `time.AfterFunc` でタイムアウトさせ、deadlock を CI で検出する）。

### F-2 [high] ハッシュチェーンが鍵なし SHA-256 で、攻撃者が全行を再計算できる

- 該当: `logger.go:66-75`（`chainHash` に鍵入力なし）、`logger.go:157-197`（`VerifyChain`）、
  `logger.go:130-153`（`loadLastHash`）。
- 攻撃シナリオ(1) **truncate + 再生成**: ログを空にする → `NewFileLogger` の起動時検証 (logger.go:42-43) は
  「空ファイル = 検証成功」なので `.legacy-*` 退避も起きない。以降、任意の偽イベントを正しいチェーンとして追記できる。
  行数・サイズの減少を検知する仕組みがプラグイン側に無い（ダッシュボードの alert_history 側には warning 検知があるが、
  security.log 側には無い）。
- 攻撃シナリオ(2) **鍵が漏れる前提**: V1(HMAC) を入れても、鍵 `/samba/share/Kizuna-Eye/keys/chain.key` は
  agent ユーザーが読める位置にあり、`scripts/attack_hmac_forge.py` は key を読んで
  `hmac.new(key, prev + go_json(entry))` を再現している。**同一ユーザー権限の攻撃者には V1 は無効**。
- 攻撃シナリオ(3) **定期検証が無い/失敗時が静か**: 検証は起動時のみ（logger.go:42-43 と、V1-A の「5分ごと」は配備版でのみ）。
  検知結果が「ログに1行出るだけ」で、通知（notify）に載るかが不明 → 見逃しやすい。
- 深刻度: **high**（証跡隠滅・偽装）
- 修正案:
  1. 鍵を **agent ユーザーが読めない** 場所へ（root:root 0600、`sudo` 経由で読む専用ヘルパー）。これが本質。
  2. ファイルサイズ/行数の単調減少を `alert_history` と同様に **critical** として通知。
  3. 「追記専用」の担保: `chattr +a` 併用、またはログ本体を別ユーザー所有ディレクトリ（0700, 別UID）へ。
  4. 鍵ローテーション手順を docs 化（既知課題）。`keys/chain.key` のローテーション時は
     `.legacy-` 退避と同じく旧鍵による検証区間を残す（鍵IDを各行に持たせる）。

### F-3 [high] 新規リッスンポート検知が PATH 依存（`ss` を差し替えられる）

- 該当: `portmon.go:117` `exec.Command("ss", "-H", "-tuln")`（V3 の絶対パス化が適用されていない）。
- 攻撃シナリオ: 攻撃者が agent ユーザーの PATH に書き込めるディレクトリ（`~/.local/bin` 等）へ
  偽 `ss`（何も出力しない）を置いて、プラグイン起動・再起動時に PATH が通れば、
  **新規リッスンポート検知が恒久的に無効化**される。`nc -l` しても無検知。
- 深刻度: **high**（検知無効化。前提は PATH の書き換え可否 → 要確認）
- 修正: `/usr/sbin/ss`（`exec.LookPath` → 絶対パス、存在しなければ起動時に ERROR 通知）に変更。
  blocker.go:48-52 は既に絶対パス + `net.ParseIP` 検証で作られているので、同じ方針を portmon にも適用する。
- 併せて確認すべき: `pkg/module/system.go:111,118,120` の `smartctl`。`exec.LookPath` 結果は PATH 依存。

### F-4 [high] FIM ベースライン自体が無保護（改ざん・検知回避の温床）

- 該当: `fim.go:199-223`（`loadBaseline` は JSON を無検証で信用）、`fim.go:148-152`（毎回上書き保存）、
  `fim.go:93-108`（`watched` に無いパスは critical ではなく warning「ベースライン追加」に格下げ）。
- 攻撃シナリオ(1): ベースライン JSON（`SecurityConfig.FIMBaselinePath`、既定はプラグインのデータディレクトリ）に
  **改ざん後のハッシュを自分で書いておく** → 次回スキャンで「変更なし」= 無検知。
- 攻撃シナリオ(2): `watched` 配列から対象パスを消す → 改ざんは `f.baseline` に無いので「新規ファイル(warning)」扱いに落ちる。
  critical 通知は出ない（気付けるが格下げ）。
- 攻撃シナリオ(3): `fim.go:68-69` 読めないファイルは `continue`。`fim.go:148` で `f.baseline = current` されるため
  **読めないファイルはベースラインからも消える**。`chmod 000` を維持すれば永続的に無検知。
- 深刻度: **high**
- 修正案:
  1. ベースラインも V1 の鍵で HMAC チェーン化（`logger.go` の仕組みを流用）。
  2. 権限: ベースラインを root:root 0600 にし、保存は `sudo -n tee` 経由（blocker.go と同じ sudo ヘルパー方式）。
  3. `chmod 000`/読取不能を**明示的に warning 通知**（silent skip をやめる）。
  4. `watched` の消失（配列が縮んだ）を critical 通知（自己監視）。

### F-5 [medium] ログ監視の offset が path 管理で inode を見ていない（ローテーション取りこぼし）

- 該当: `monitor.go:40-43`（`lineSource{path, offset}`）、`monitor.go:107-119`（初回 offset = 現在サイズ）。
- 攻撃シナリオ: `logrotate`/`systemctl restart`/手動 truncate の直後に攻撃アクションを実行。
  新しいファイルは古い offset より小さいため、Seek 位置以降が空 → **その区間の行が読まれない**。
  既知課題として `docs/WORK_RECORD_2026-10-03_part10.md:63`「ログローテーションの inode 追跡 — 未実装」。
- 深刻度: **medium**（検知の穴。ローテーション直後という限定的な時間窓）
- 修正: `syscall.Stat_t` の `Dev`/`Ino` を `lineSource` に保持し、変化したら `offset=0` にリセット。
  併せて `!NotExist` 時の挙動（ファイル一時消失）も明示的に扱う。
- 注意: `monitor.go:121` は `O_NOFOLLOW` 使用（良い）。ここは symlink 攻撃への配慮が既にある。

### F-6 [medium] ログ偽装（誤検知 flooding）でアラート履歴を埋められる / 本物を押し出せる

- 該当: `monitor.go:28-37` の正規表現は **sshd/sudo/dpkg のログ書式をそのまま信用**してパースする。
  `reSSHLogin` は `Accepted password for root from <ip>` に一致すれば「ログイン成功」critical として通知する。
- 攻撃シナリオ: 監視対象 `WatchFiles` に追記可能なファイルが1つでもあると（例: アプリログを監視している場合）、
  1行の偽装文字列を書くだけで **偽の critical ログイン成功通知が飛ぶ**。大量に書けば通知疲れ（alert fatigue）を起こさせ、
  本物の攻撃を見逃させる。「監視対象ファイルへの書き込み権限」が前提だが、`/var/log/` 配下を agent が読む構成では
  同居アプリが書き込めることが多い。
- 軽減策（本体側に既にある良い仕組み）: `pkg/alert/history.go:27-31` の `Trusted` フラグ。
  未信頼アラートを先に追い出す設計は正しい。ただし**通知そのものは抑制されない**。
- 深刻度: **medium**（検知妨害・通知スパム。V5 の journald 照合はこの対策）
- 修正案:
  1. V5 の journald 照合を「critical レベル以上」に必須化（`journalctl -o json` で同一時刻・同一内容の行を確認できた場合のみ critical、無ければ warning に降格）。
  2. IP ごとのレート制限（`BurstWindow` の流用）と、単位時間あたりの通知上限。
  3. sshd の実ログ（`/var/log/auth.log`）が `WatchFiles` に含まれているかを起動時に検査し、
     `Accepted` を扱うなら journald 照合必須の設定バリデーションを入れる。

---

## 2. タスク1: Kizuna-Eye 本体（ダッシュボード / Agent）側のホール

### G-1 [critical] 認証無効モード（`auth.enabled=false`）では module 登録経由で RCE が可能

- 該当: `internal/auth/middleware.go:276-286`。auth 無効時は**プラグイン upload だけ**を 403 で拒否し、
  残りは全部 `next.ServeHTTP`。`middleware.go:48-57` のコメント自身が
  「module 登録は plugin_path を渡す = .so をロードする **code-execution boundary**」と認めている。
  つまり `POST /api/modules` / `PUT /api/modules/{name}` / `POST /api/modules/reload` が未認証で叩ける。
- 攻撃シナリオ: `curl -X PUT http://192.168.0.233:8080/api/modules/xxx -d '{"plugin_path":"/tmp/evil.so"}'`
  → `POST /api/modules/reload` → Agent が `/tmp/evil.so` をロード = **未認証 RCE**。
  `/api/config` の PUT（設定書き換え）も同様に開放される。
- 深刻度: **critical**（認証無効で運用している場合）。信頼網内運用でも「同じ LAN の1台が侵害されたら終わり」。
- 修正案:
  1. `isPluginUploadPath` の判定を `/api/plugins`・`/api/modules`（書き込みメソッド）・`/api/config`（PUT）・
     `/api/auth/*`（アカウント操作）まで広げ、auth 無効時は **「読み取り系のみ許可」** の allowlist 方式にする
     （denylist ではなく allowlist）。
  2. auth 無効時は起動ログに **WARN を必ず出す**（現在は無言に近い）。可能なら `--insecure-no-auth` のような明示フラグを要求。

### G-2 [high] Agent トークンをクエリ文字列で受理 + トークン未設定時は検証ごとスキップ

- 該当: `cmd/dashboard/main.go:857-868`（`?token=` をフォールバックで読む）、`main.go:927` / `main.go:995-1007`
  （`eligibleForHeuristicPromotion` → `promote` → `isAgent = true`）、`main.go:956-969`
  （`isAgent` のイベントを `hub.onEventFn` に流し、ブラウザへ broadcast）。
- 攻撃シナリオ(1): クエリのトークンはアクセスログ・リバースプロキシログ・`Referer` に残る。
  Nginx の access log を読める、あるいは `Referer` を外部へ送らせられれば Agent トークンを奪える。
- 攻撃シナリオ(2): `hub.agentToken == ""`（未設定）の構成では `main.go:857` の if が偽なので検証を丸ごと素通り。
  Origin ヘッダを送らない非ブラウザクライアント（`curl`・python-websocket）は
  **ステータス様 JSON を1回送るだけで Agent に昇格**し、以後
  `{"event":"security_alert", ...}` を送れば `onEventFn` 経由でアラート履歴に偽イベントを注入できる。
  ＝ **ログ偽装 + 偽アラート**。
- 深刻度: **high**（認証バイパス・偽装。`agentToken` 未設定なら critical）
- 修正案:
  1. クエリからのトークン受理を削除（`X-Kizuna-Agent-Token` ヘッダのみ）。互換のため一時的に受ける場合も WARN ログ。
  2. **ヒューリスティック昇格の完全撤去**。`agentToken` 未設定で `/ws` を使用するなら起動時にエラーで落とす
     （もしくはランダム生成して agent_config.json に書き戻す）。
  3. `eligibleForHeuristicPromotion` / `acceptAgentPayload` の定義（未読）を必ず確認。
- 検証コマンド（読み取りのみ）: `grep -n 'agentToken\|heuristic\|eligibleFor' cmd/dashboard/main.go`

### G-3 [high] プラグイン `.so` はアップロード時点で実行されうる（検疫の破綻）

- 該当: `internal/api/plugins.go:639-691` `inspectPlugin` は `plugin-inspect --so <path>` を実行し、
  その JSON 出力から `display_name` / `fields` / `is_backup` を取得している。
  Go の plugin は `plugin.Open` の時点で `init()` とパッケージ変数初期化が走るため、
  **メタ情報を読む = コードをロードする = 実行する** ことになる。
- 攻撃シナリオ: admin 権限（または G-1 により未認証）で `POST /api/plugins/upload` に悪性 `.so` を送るだけで、
  **登録・reload を待たずに**ダッシュボード権限で任意コードが走る（`plugin-inspect` プロセス内、タイムアウト10秒 / plugins.go:643）。
- 深刻度: **high**（供給網。`plugin-inspect` はサブプロセスなので agent 本体は守られるが、
  設定ファイル・鍵・トークンは同じユーザーが読める）
- 修正案:
  1. `plugin-inspect` を **seccomp/landlock + ネットワーク無効 + 専用ユーザー** の隔離サンドボックスで実行。
  2. 署名検証を実装してから `.so` を `pluginsDir` に置く（検疫ディレクトリで inspect → 署名OK → 移動、の順序にする）。
  3. 現状はアップロード先 = 実行対象ディレクトリ。**隔離ディレクトリへ一旦保存 → 検査 → 移動** に変更。
- 要確認: `cmd/plugin-inspect/main.go` が `plugin.Open` を使っているか（`grep -n 'plugin.Open' cmd/plugin-inspect/main.go`）。

### G-4 [medium] ロール判定が生の `r.URL.Path` 前方一致（正規化なし）

- 該当: `internal/auth/middleware.go:253-263` `minRoleFor`。`path.Clean` を通していない。
  `middleware.go:262` は**どの規則にも一致しないパスを `RoleViewer` にフォールバック**させる。
- 攻撃シナリオ: `/api/logs/../config` のようなパスはどの規則にも一致せず viewer 相当と判定される。
  現状は Go の `ServeMux` が未クリーンなパスを 301 リダイレクトするため直接のバイパスにはならないと考えるが、
  **防御が ServeMux の実装詳細に依存**している。将来 `http.Handler` を差し替えると穴になる。
- 深刻度: **medium**（defense-in-depth。現時点で実害は未確認）
- 修正案: `minRoleFor` / `isPublicPath` / `plugin upload` 判定の手前で `path.Clean(r.URL.Path)` を使用。
  未知のパスは **deny（403）** にする（viewer フォールバックをやめる）。
- 明日の確認: `curl -i 'http://192.168.0.233:8080/api/logs/../config'` → 301/403 ならOK。**200 が返ったら critical**。

### G-5 [medium] ログイン試行レート制限の弱点（分散攻撃・map 膨張・bcrypt DoS）

- 該当: `internal/auth/ratelimit.go:13-22`（IP 単位の in-memory map）、`ratelimit.go:139-153`（`RecordFailure` は
  失敗時にエントリを**無制限に作る**）、`ratelimit.go:170-187`（reaper は10分間隔）。
- 攻撃シナリオ(1) **分散総当たり**: 制限は IP 単位のみ。ユーザー単位・グローバル単位のカウンタが無いので、
  IP を変えれば（IPv6 なら事実上無限）試行できる。`BCrypt DefaultCost=10` は1回あたり数十ms なので、
  並列10で **CPU 枯渇 DoS** にもなる（ログイン処理は認証前に走る）。
- 攻撃シナリオ(2) **map 膨張**: 送信元を偽装した大量 IP で `attempts` map が肥大（10分は保持される）。
  `monitor.go` 側は `maxFailTrackers` 等の上限を持つ実装なので、同じ設計を auth 側にも適用する。
- 深刻度: **medium**（DoS・総当たり耐性）
- 修正案:
  1. ユーザー名単位 + グローバルな失敗カウンタを追加（1ユーザーN回/10分、全体M回/分）。
  2. `attempts` map に上限（例 10000）+ 追い出し。
  3. ログイン処理の同時実行数制限（セマフォ。例 8 並列）。
- 補足（良い点）: `clientIP` は loopback のときだけ `X-Forwarded-For` を信用する（ratelimit.go:62-84）。
  ただし**同じホストの別ユーザー**が loopback 経由で XFF を自由に振れる点は残る（マルチユーザー機なら
  ロックアウト回避に使える）。`trusted_proxies` の明示設定を推奨。

### G-6 [medium] alert_history の永続化が `O_NOFOLLOW` なし + 整合性検証の所在が不明

- 該当: `pkg/alert/history.go:124` `os.OpenFile(path, O_CREATE|O_WRONLY|O_APPEND, 0600)`（**O_NOFOLLOW なし**）。
  同じリポジトリの `logger.go:35` は O_NOFOLLOW 付きなので、実装の不揃い。
- 攻撃シナリオ: `alert_history.jsonl` がシンボリックリンクに置き換え可能な配置（同じユーザーが書けるディレクトリ）なら、
  `/etc/...` など任意ファイルへ追記させられる（内容は JSON 1行なので確実性は低いが、ログ汚染には十分）。
- 深刻度: **medium**（`h.path` のディレクトリ権限に依存）
- 併せて: `history.go:234-269` の `Load` は JSON を無検証で読む。V2 の「in-place 改ざん=critical / 行数減=warning」を
  実装しているコードがこのリポジトリに見当たらない（`patch_v1v2.py` はプラグイン側を対象）。
  → **V2 はどのファイルに入っているのか要確認**（`grep -rn 'in-place\|行数' /samba/share/Kizuna-Eye` / プラグイン側）。
- 修正案: `O_NOFOLLOW` 付与。`Clear()` のアーカイブ（history.go:290-315）と同じ粒度で、
  起動時+定期の行数/サイズ監視を追加（減少は critical）。

---

## 3. タスク2: V1〜V5 の有効性検証（現時点の判定）

**結論（先）**: このリポジトリにあるコードだけでは **V1/V2/V4/V5 の検証は不能**。
V3 のみ、repo 内 `scripts/kizuna-cron-read.sh`（573B、絶対パス化済み）と
`docs/WORK_RECORD_2026-10-03_part11.md:16` の検証記録で「意図どおり」であることを確認できる。
V1/V2/V4/V5 は配備版（`/samba/share/Kizuna-Security/plugin/*.go`）を持ち込んでもらえれば
本日中に判定できる。以下、各項目の「いま分かること」。

### V1（HMAC-SHA256 + 定期検証）
- 検証可否: **不可**（snapshot の `logger.go` は鍵なし SHA-256。`NewFileLoggerKeyed` 不在。
  `patch_v1v2.py:38,48,72` が追加を予定している実装が正）。
- いま分かる有効性: **同一ユーザー攻撃者には無効**。鍵 `/samba/share/Kizuna-Eye/keys/chain.key` は
  agent ユーザーが読める場所にあり、`scripts/attack_hmac_forge.py:17` は
  `hmac.new(key, prev + go_json(entry))` で偽造を再現している。**設計上の前提（別ユーザー化）が未達なら V1 は無効**。
- 残る穴（実装を見るまでもなく確定）:
  1. **鍵のローテーション手段が無い** → 漏洩時の被害が恒久化。各行に鍵IDが無いため、鍵を替えると
     過去ログの検証が破綻する（`.legacy-` 退避頼みになる）。
  2. **削除/truncate の検知が「ハッシュ不一致」頼み** → 末尾を削って `prev_hash` を合わせた
     self-consistent な状態は検知できない（行数・サイズの単調性チェックが必要。G-6/F-2 参照）。
  3. 定期検証の結果が **通知経路に乗るか**（`notify` に流れるか、ログ1行だけか）が未確認。
     ログ1行だけなら誰も気付かない。
- 判定に必要なもの: `logger.go` の配備版、`config.go` の `ChainKeyPath` 既定とパーミッション、5分検証の呼び出し元と通知経路、
  `ls -l /samba/share/Kizuna-Eye/keys/chain.key` の実行結果。

### V2（alert_history 整合性）
- 検証可否: **不可**。repo の `pkg/alert/history.go` に整合性チェックは無い（`Load` は無検証: history.go:234-269）。
  `docs/WORK_RECORD_2026-10-03_part11.md:40` は「V2-B 実装済み」と書くが、該当コードが repo に存在しない
  → 配備版プラグイン（`filelog` 相当）にあると推定。
- 精度への懸念: 「in-place 改ざん=critical / 行数減=warning」という**行数ベース**の判定は、
  行を **追加** された場合（偽アラート注入）や、**行数を保ったまま内容を入れ替えた**場合に沈黙する。
  ハッシュチェーン（V1）と組み合わせないと「追加型の偽装」を検知できない。
- 判定に必要なもの: V2 の実装ファイル・関数名（`grep -rn 'in-place\|integrity\|行数'` の結果）と、
  偽アラートを1行追記した後の検知ログ。

### V3（ヘルパー絶対パス化）
- 検証可否: **可（repo 内で確認済み）**。`scripts/kizuna-cron-read.sh` は `/usr/bin/basename`,
  `/usr/bin/sha256sum`, `/usr/bin/cut`, `/usr/bin/printf` を使用。設置物は root:root 755 で実測 EXIT=0
  （`docs/WORK_RECORD_2026-10-03_part11.md:16-18`）。
- **ただし V3 は検知のごく一部しか守っていない**。プラグイン本体は依然 PATH 依存で、
  `portmon.go:117 exec.Command("ss", ...)`、`pkg/module/system.go:111,118,120 smartctl` は PATH 経由。
  **「V3 完了」をもって PATH 乗っ取り耐性が完了したと判断してはいけない**（F-3）。
- 追加で潰すべき: cron 側は絶対パスでよいが、**systemd ユニットの `Environment=PATH=`** と
  プラグイン起動時の PATH を確認（`systemd/kizuna-eye.service` を要確認）。
- 判定に必要なもの: `/usr/local/bin/kizuna-cron-read.sh` の `ls -l` と `crontab -l`、
  プラグイン systemd ユニットの `Environment`。

### V4（偽 Agent 接続検知のレート制限）
- 検証可否: **不可**（`hub.noteAuthReject` の実装が未読: `cmd/dashboard/main.go:865,931` から呼ばれる）。
- いま分かる問題（レート制限の妥当性以前の話）:
  1. **そもそも昇格が通る**（G-2）。`agentToken` 未設定なら検証自体がスキップされ、
     非ブラウザクライアントが status を1回送るだけで Agent 昇格する。この構成では
     「偽 Agent の検知」は成立しない。
  2. トークンを `?token=` で受けるため、**正規トークンを知っている攻撃者は「不一致」を出さない**。
     つまり「不一致回数による検知」はトークン未所持の攻撃者しか捕まえない（当然だが、期待値の明確化が必要）。
  3. 判定材料（`noteAuthReject` の窓・閾値・キーが IP か否か）が未確認。IP キーなら偽装 IP で簡単に回避。
- 妥当性の目安（提案値）: 同一 IP から 5回/60秒 で WARN、20回/300秒 で当該 IP を一定時間 `/ws` から拒否（429）、
  かつ**全体で** 200回/300秒を超えたら critical 通知（分散時の保険）。閾値は `dashboard_config.json` で調整可能に。
- 判定に必要なもの: `noteAuthReject` の定義（`grep -n -A30 'func.*noteAuthReject' cmd/dashboard/main.go`）。

### V5（ログインジェクション検知 = journald 照合）
- 検証可否: **不可**。snapshot の `monitor.go:28-31, 471-509` は sshd ログを正規表現で読むだけで、
  journald 照合が無い。`docs/WORK_RECORD_2026-10-03_part13b.md` 以降に記載があると推定。
- 精度への懸念（照合を入れるなら考慮必須）:
  1. **時刻の丸め**: journald のエントリはマイクロ秒精度、ログ行の `ts` は秒精度。
     照合は「同一秒窓 ± N秒」で行わないと**偽陰性（本物を偽物と判定）**が多発する。
  2. **正規化**: journald の `MESSAGE` は sshd が書く前の生メッセージとは微妙に異なる
     （`_COMM=sshd` の有無、PID 接頭辞、`sudo:` の前置き）。正規表現を2系統（ログ用・journald用）に分けて、
     **「ユーザー名 + 送信元IP + メッセージ種別」の3点一致**で照合するのが安全。
  3. **journald 側の権限**: `journalctl` を agent ユーザーが実行できるか（`systemd-journal` グループ）。
     実行できないと照合が常に失敗し、**全 critical が warning に降格**して検知力が落ちる。
     → 照合不能時は「判定不能」として critical を維持する（fail-open にしない）。
  4. ログインジェクション（改行・制御文字の埋め込み）自体への対策は、
     **ログをそのまま通知本文に埋めない**こと（`messages.go` の `msg()` 展開を要確認。
     `%` や ANSI エスケープが通知・UI にそのまま出ると、通知偽装や端末制御に使われる）。
- 判定に必要なもの: V5 の実装（journald 照合関数）、`messages.go` のエスケープ処理、
  `journalctl` の実行権限（`id <agent-user>` / `groups`）。

---

## 4. 明日つぶす優先順位（上から）

| # | 対象 | 内容 | 期待効果 |
|---|---|---|---|
| 1 | `fim.go`（配備版） | `langMu` 分離が適用済みか確認。未適用なら `patch_fim_deadlock.py` を適用 | FIM 停止（F-1）を解消。これが未適用だと**他の検証結果が全部無意味になる**（検知が死んでいる） |
| 2 | `cmd/dashboard/main.go` | `agentToken` を必ず設定。`?token=` 受理を削除。ヒューリスティック昇格を撤去 | 偽 Agent 昇格・偽アラート注入（G-2 / V4） |
| 3 | auth 無効運用の有無 | `dashboard_config.json` の `auth.enabled` を確認。false なら module 書き込みを allowlist で塞ぐ | 未認証 RCE（G-1） |
| 4 | `portmon.go` / `system.go` | `ss` / `smartctl` を絶対パス化 | PATH 乗っ取りによる検知無効化（F-3） |
| 5 | `logger.go` / V1 配備版 | 鍵の置き場所（agent から読めるか）、5分検証の通知経路、行数単調減少の検知 | 証跡隠滅（F-2 / V1） |
| 6 | `fim.go` ベースライン | HMAC 化 + root 所有 + 読取不能の警告 | 改ざん後の無検知（F-4） |
| 7 | `monitor.go` | inode/Dev 追跡、journald 照合の fail-open 禁止、通知レート制限 | ローテーション取りこぼし・偽装 flooding（F-5/F-6/V5） |
| 8 | `plugins.go` / `plugin-inspect` | 隔離ディレクトリ検疫 → 署名検証 → 移動 | アップロード時 RCE（G-3） |
| 9 | `history.go` / `middleware.go` | `O_NOFOLLOW`、`path.Clean`、viewer フォールバック廃止 | 情報漏洩・改ざんの残穴（G-4/G-6） |
| 10 | `ratelimit.go` | ユーザー/グローバル単位の制限、map 上限、同時実行制限 | 分散総当たり・CPU DoS（G-5） |

---

## 5. 検知カバレッジ確認表（タスク3の代替。攻撃コマンドは書かない）

「あなたが実行し、私が判定する」ための判定基準。**判定は `kizuna-security.log` の該当行の有無**で行う。

| 攻撃（あなたが実施） | 期待される検知 | 判定基準（FAIL の条件） |
|---|---|---|
| 新規リッスンポート | `category=listen_port`, level=critical/warning | 60秒以内に検知ログが無い / 偽 `ss` を置いた状態で更に検知が消える |
| cron 変更 | `category=cron` | 変更から 60秒以内に無い。`/etc/cron.d` 追加が無検知 |
| SUID/SGID 付与 | `category=suid` | baseline 差分が出ない。既知 SUID 一覧の更新が無い |
| SSH ログイン（失敗連続） | `category=ssh_failed` → `FailedBurst` で critical | burst 閾値到達で critical にならない。journald 照合が false-negative を出す |
| FIM 改ざん | `category=integrity`, level=critical | **無反応なら F-1 のデッドロック確定**（最優先で潰す） |
| （追加）検知の停止そのもの | 監視プロセスの生存 | 攻撃中に `kizuna-security.log` の更新が止まったら、それが最大の脆弱性 |

> 追記（2026-10-04）: 上表 4 行目（SSH ログイン失敗連続）は実機の攻撃テストで
> 「全件 warning 止まり・critical に到達しない」ことを確認した（`failed_burst` を
> 最初の失敗からの固定窓で数えていたため、20〜80 秒間隔の試行では到達不能）。
> スライディング窓・持続攻撃（低頻度）・ユーザー名の列挙の 3 規則へ改め、
> `docs/session_hardening_a1_a4_20261003.md` の「追記（2026-10-04）: 攻撃 4」に
> 修正と検証結果を記録した。6 行目（F-6 通知疲れ）の原因だった「ログイン成功の
> 急増」の再通知も IP ごとにレート制限した。

### ログを送ってもらえれば私がやること
1. `kizuna-security.log`（当日分）と `alert_history.jsonl` を貼る → カテゴリ別の集計、未検知・誤検知候補の特定。
2. 「攻撃 → 検知されなかった行」をコードのどの分岐で落ちたかまで特定（例: 正規表現不一致なら行のフォーマットを提示）。
3. 特定した原因に対して**修正パッチ（Go）＋回帰テスト**を書く。

---

## 6. タスク3（攻撃スクリプトの雛形）について

**作成しません。** 理由は「攻撃」か「防御テスト」かの呼び方ではなく、成果物が
**特定ホスト（192.168.0.233）に対する実行可能な攻撃ツール**（SUID 作成・SSH 総当たり・ログ/FIM 改ざんを含む）である点が変わりません。
これは以前のご依頼と同じもので、立場を変えていません。

代わりに、**あなたが書いたスクリプトを私が読んでレビューする**（副作用・後片付けの抜け・検知側への影響の指摘）
ことはできます。また、上記の検知カバレッジ表に沿って **あなたが実行した後のログを解析し、検知の穴をコードで塞ぐ**
のが、この環境（Linux 実行環境なし）で私が最大の価値を出せる分担です。

---

## 7. 未読ファイル（追加のホールが眠っている可能性が高い順）

読解して追加報告すべきファイル（まだ読んでいない）:

1. `_pentest_backup_20261003_005910/Kizuna-Security-plugin/config.go`（既定値・秘密情報・WatchFiles の既定）
2. 同 `plugin.go`（run ループ、FIM/Monitor の呼び出し順、goroutine 構成）
3. 同 `cronmon.go`, `suid.go`, `portmon.go`, `blocker.go`（`sudo -n` の権限境界。blocker は既に絶対パス+IP検証で良い）
4. 同 `messages.go`（`msg()` のフォーマット展開 → ログ由来文字列のインジェクション）
5. `internal/api/logs.go`（ログ読み出しのパス検証 / SSE ストリームの DoS）
6. `internal/api/config.go`（秘密情報のマスク漏れ、書き込み先パス）
7. `internal/api/modules.go`（`plugin_path` の検証。G-1 の実害範囲）
8. `internal/auth/handlers.go` / `session.go` / `store.go`（セッション固定・失効、アバターのパス処理）
9. `internal/updater/updater.go:160-170`（`exec.Command(script)`。更新スクリプトの署名検証の有無）
10. `pkg/config/config.go`, `pkg/config/urlcheck.go`（`NotifyURL` の SSRF 検証）
11. `cmd/agent/main.go`（agent 側の plugin_path 解決: `resolveAndCheckPluginPath`、実行ユーザー）
12. `scripts/kizuna-cron-read.sh`（V3 の絶対パス完全性の再確認。`readlink`/`stat` など未確認コマンドの有無）

**お願い**: 配備版 `/samba/share/Kizuna-Security/plugin/*.go`（特に `logger.go`, `fim.go`, `config.go`,
`monitor.go`, `filelog` 相当）と `/samba/share/Kizuna-Eye/keys/` のパーミッション、
`dashboard_config.json` の `auth.enabled` / `agent_token` 有無を教えていただければ、
V1/V2/V4/V5 の判定を今日中に確定できます。

---

## 8. リポジトリ衛生（今回の副次的な発見）

- プラグイン実体が版管理外（0-2 参照）。**セキュリティ製品の本体コードが git に無い**状態は、
  V1〜V5 の検証・回帰の妨げになるだけでなく、改ざん検知の観点でも穴（攻撃者がソースを書き換えても差分が出ない）。
  `plugins/Kizuna-Security/` として取り込み、`.so` と鍵だけを ignore する構成を推奨。
- `_pentest_backup_*/` が3世代分ワークツリーに残っている（うち1つは今日の日付）。
  監査対象がどれか分からなくなるので、確定したスナップショットだけ残して他は削除/アーカイブ推奨。
- `keys/chain.key` は `.gitignore` の `keys/` により**未追跡**（`git ls-files` で確認済み）＝ 良い。
  `users.json` / `sessions.json` / `agent_config.json` / `dashboard_config.json` / `logs/` も未追跡を確認済み。

---

## 9. 修正適用記録（2026-10-04 / 続き17）

デプロイ済みのソースを実読して再判定し、F-1 / F-2(HMACチェーン) / F-6(V5 spoof) / G-1 / G-3(緩和) は
**既に対策済み**であることを確認した。残る指摘にソース修正を適用した
（修正内容の全文・検証ログは Samba 作業記録 第130章を参照）。

| 項目 | ファイル | 対応 |
|---|---|---|
| F-3 | `Kizuna-Security/plugin/portmon.go` | `ss` を絶対パスで解決（`resolveSSPath`）。失敗/不在は warning を1回通知 |
| F-4 | `plugin/fim.go`, `plugin/plugin.go` | ベースラインを鍵で署名し、未署名/不一致は critical＋再取得。読み取り不能は警告しベースラインから消さない |
| F-5 | `plugin/monitor.go` | `lineSource` に dev/ino を追加し、ローテーション検知で先頭から再読込 |
| F-2 | `plugin/integrity.go` | ログ行数の最大値を記録し末尾削除・消失を critical 検知。鍵の権限を warning |
| G-2 | `cmd/dashboard/main.go` | Agent トークンのクエリ文字列（`?token=`）受け付けを削除しヘッダのみに |
| G-4 | `internal/auth/middleware.go` | `canonicalPath()` で正規化してからロール判定（別名による権限すり抜けを封鎖） |
| G-5 | `internal/auth/ratelimit.go` | attempts マップに上限 10000＋古い順の削除を追加 |
| G-6 | `pkg/alert/history.go` (+`nofollow_*.go`) | 追記 open に O_NOFOLLOW（Linux、他 OS はフォールバック） |

検証（実行済み）: プラグイン `GOOS=linux go build/vet` = exit 0、Kizuna-Eye `GOOS=linux go build` = exit 0、
`go test ./...`（Windows 実行）= 全パッケージ ok（新規 G-4/G-5 テストを含む）。
プラグインの新規テスト（F-2/F-3/F-4/F-5）は Linux 実機での実行が必要（本セッションでは未実行）。
`.so` と dashboard バイナリのビルド・配置も Linux 側での実施が必要。

残存リスク: FIM 状態ファイルとチェーン鍵は agent 実行ユーザーの所有物であるため、その権限を
奪われた場合の書き換えは「検知はできるが防止はできない」（agent 専用ユーザー化が本質的対策）。
G-3 の `plugin-inspect` は設計上 `.so` をロードするため、悪意あるプラグインの検査時コード実行は
緩和のみ（署名検証・分離実行は未実装）。

---

## 10. ファイル保護の適用と SMB 露出の判定（2026-10-04 / 続き18）

### 10.1 chmod 適用結果（サーバ1 `/samba/share/Kizuna-Eye` = `Z:\Kizuna-Eye`）

アプリは `cwd=/samba/share/Kizuna-Eye` / `uid=1000(user)` で稼働し、`-config dashboard_config.json`（相対）が
読むのは**共有上の実ファイル**であることを確認した（`/opt/kizuna-eye/bin/` に設定は無い。
プロセス: `/opt/kizuna-eye/bin/dashboard_linux`, `/opt/kizuna-eye/bin/agent_linux`）。

| 対象 | Before | After |
|---|---|---|
| `keys/chain.key` | 600 | 600 |
| `dashboard_config.json` | 600 | 600 |
| `agent_config.json` | 600 | 600 |
| `users.json` / `sessions.json` | 600 | 600 |
| `keys/`（ディレクトリ） | 700 | 700 |
| `logs/`（ディレクトリ） | 775 | **700** |
| `logs/` 配下 全ファイル（19件） | 664/644 が4件（`dashboard.log`, `dashboard_linux.pid`, `agent_linux.pid`, `kizuna-backup-lite.log`） | **全て 600** |

追加で締めた箇所: `_pentest_backup_20261002_065148/`（`dashboard_config.json`/`agent_config.json`/
`users.json`/`sessions.json` の複製と、グループ書込可だった `kizuna_watchdog.so` 664 → 600）、
`/opt/kizuna-eye/bin/plugins/kizuna_security.meta.json` 644 → 600。
`*.example.json` は秘密を含まないテンプレートのため 644 のまま（git 追跡対象）。

### 10.2 【重大】`[Share-1TB]` はゲスト書込可のため chmod 600 では守れない

`/etc/samba/smb.conf`:

```ini
[Share-1TB]
   path = /samba/share
   read only = no
   guest ok = yes
   create mask = 0777
   directory mask = 0777
   force user = user
```

LAN 上の誰でも（匿名ゲストで）`uid 1000(user)` として共有をマウントできる。`force user` により
**SMB 経由のアクセスも所有者 uid 1000 になるため、chmod 600 は無効**（権限ではなく共有設定の問題）。
影響:

1. `dashboard_config.json`（Agent トークン / Webhook URL / セッション秘密鍵）、`agent_config.json`、
   `users.json`（bcrypt ハッシュ）、`sessions.json`、`keys/chain.key`（HMAC 鍵）が LAN から読める。
2. HMAC チェーン（F-2/F-4）は**鍵漏洩前提**になり、任意の改ざんログを正当な署名付きとして作成できる。
3. `web/` 書込可 = 認証ページ/JS 改ざん（格納型 XSS）、`scripts/`・`start.sh`・`modules.json` 書込可
   = 次回起動/更新時コード実行、`logs/` 書込可 = 監査ログ改ざん（F-2 は検知できるが防止できない）。
4. 対策（**要 sudo**。本セッションでは未実施）:
   - `guest ok = no` / `valid users = user` / `map to guest = never`、`create mask = 0600`、
     `directory mask = 0700` → `sudo testparm -s` → `sudo systemctl restart smbd`。
   - より根本的には秘密を共有外（例 `/opt/kizuna-eye/data/`）へ移し、`-config` を絶対パス化する。
   - `keys/chain.key` は漏洩前提でローテーションする。

### 10.3 `.gitignore` の不整合を統合

インデックス（ステージ済み）の `.gitignore` は堅牢化済み（`scripts/.ssh_env`, `*.log.*`, `avatars/`,
`modules.json`, `*.meta.json`, `plugins/`, ルート限定ビルド成果物, `sessions.json.*`）だったが、
**作業ツリー側が古い弱い版に戻っており**、`git add -A` で混入し得る状態だった（`git check-ignore` が rc=1、
`git status` に `??` で出現）:

- `?? scripts/.ssh_env`（**SSH パスワードを含む**）
- `?? modules.json`（実行時構成）
- `?? dashboard.log.1`（ローテーション後ログ。`*.log` は一致しない）
- `?? avatars/shige-*.webp`（アップロード画像 = 実行時データ。公開リポジトリに出る）

ステージ済み版を作業ツリーへ復元したうえで不足パターンをマージ（85行）。
結果、`keys/chain.key` / 4種の json / `logs/*` / `scripts/.ssh_env` / `modules.json` / `dashboard.log.1` /
`avatars/*` / `tmp/*` / `_pentest_backup_*/` はすべて IGNORED、`*.example.json` 3種のみ TRACKABLE（意図通り）。
バックアップ: サーバ側 `/tmp/gitignore.worktree.before-merge.20261004`, `/tmp/gitignore.index.restored.20261004`。

### 10.4 検証（サーバ側で実施）

- `ls -la`: `keys/chain.key` 600、`keys/`・`logs/` は 700、`logs/` 配下全件 600。
- `git status --short -- keys dashboard_config.json agent_config.json users.json sessions.json logs` = **空**。
- `git ls-files`（chain.key / 各 config json / users.json / sessions.json / logs/ / ssh_env）= **0件**。
  追跡下の json は `agent_config.example.json` / `dashboard_config.example.json` / `modules.json.example` のみ。
- `find -perm /077`（*.json|jsonl|log|key|pid）= 残りは `*.example.json` 2件のみ。
- chmod 後もアプリは稼働継続（`logs/agent.log`・`logs/dashboard.log` の mtime が現在時刻で更新）。

### 10.5 残タスク

- `smb.conf` のゲスト無効化（10.2）。sudo パスワードが必要なため未実施。
- ログ/pid を再生成する側（`start.sh` 系）の `umask` 見直し（次回生成時に 664/644 に戻る）。
- 5ファイルのコミット（`docs/session_security_audit_20261003.md`, `internal/auth/hardening_test.go`,
  `pkg/alert/nofollow_{linux_test,other,unix}.go`）＋ `.gitignore`（`git add .gitignore` が必要）。
- （持ち越し）Linux 実機でのプラグイン `CGO_ENABLED=1 go test ./...` と `.so` / dashboard のビルド・配置。
  **今回 SSH（`user@192.168.0.233`・鍵認証）が利用可能と判明したため、実機テストは実行可能になった。**

（以上）
