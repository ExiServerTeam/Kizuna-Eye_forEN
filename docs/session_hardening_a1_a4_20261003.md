# 優先度A（セキュリティ仕上げ）A-1〜A-4 実施・設計記録（2026-10-03）

対象: Kizuna-Eye（dashboard / agent / Kizuna-Security プラグイン）
前提: 実機 `<cluster-user>@192.168.0.233`（Ubuntu / 非 root 運用 / Samba 共有 `/samba/share`）

---

## A-1 フロントエンド XSS 棚卸しと修正（実施済み・稼働反映済み）

### 1. 棚卸し結果（innerHTML 使用箇所）

| ファイル | 行 | 用途 | 判定 |
|---|---|---|---|
| app.js | 280, 670, 696, 710, 759, 827, 830, 944, 971, 1220, 1224, 1625, 1658 | トースト/CPU/ディスク/ストレージ/プロセス/プラグイン/アラート | プラグイン由来値はすべて `escapeHtml`/`escapeAttr` 済み |
| modules.js | 476, 655, 729, 741, 911 | モジュール一覧・設定モーダル | 同上（`renderConfigForm`/`renderField` は `escapeHtml`/`escapeAttr` 済み） |
| config-editor.js | 323 | `guiBody.innerHTML = ''`（クリアのみ） | 本体は `createElement` + `textContent`（安全） |
| users.js | 40 | `tbody.innerHTML = ''`（クリアのみ） | 本体は `createElement` + `textContent`（安全） |

- `escapeHtml`/`escapeAttr` は既に `web/static/escape.js` に一元化されており、重複定義は **0 件**（`scripts/check_xss.js` で機械検証）。
- インラインイベントハンドラ（`onclick=` 等）と `javascript:` URL は **0 件**（CSP を骨抜きにする書き方は無し）。

### 2. 今回修正した箇所（未ラップだった属性値補間）

理由: `class="..."` / `href="..."` へデータ由来値をそのまま埋める箇所が残っていた。
現時点の値は固定集合（ホワイトリスト）由来で実害は無いが、将来の値追加で
属性から抜け出せる余地を残さないため、**クラス名専用の無害化関数**を追加して統一した。

- `web/static/escape.js`
  - 追加: `cssClass(value)` — `[A-Za-z0-9_-]` と空白以外を除去（引用符・山括弧・`/` が消えるため
    `class="..."` からの脱出も複数クラスの偽装も不可）。`global.cssClass` / `KizunaEscape.cls` /
    `module.exports.cssClass` として公開。
- `web/static/app.js`（8 箇所）
  - 688 `${index}` → `${Number(index)}`（canvas id, 数値確定）
  - 736 `disk-health ${healthClass}` → `${cssClass(healthClass)}`
  - 753 `class="${freeClass}"` → `class="${cssClass(freeClass)}"`
  - 803 `disk-health ${hi.cls}` → `${cssClass(hi.cls)}`
  - 965 `col-cpu ${cpuClass}` → `${cssClass(cpuClass)}`
  - 1199 `href="${href}"` → `href="${escapeAttr(href)}"`
  - 1203 `plugin-row-state ${stateCls}` → `${cssClass(stateCls)}`
  - 1644 `alert-source-badge ${cls}` → `${cssClass(cls)}` / 1647-1648 `${escapeHtml(level)}` → `${cssClass(level)}`
- `web/static/modules.js`（3 箇所）
  - 605 `status-dot ${statusDotClass}` → `${cssClass(statusDotClass)}`
  - 630 `badge ${versionBadge}` → `${cssClass(versionBadge)}`
  - 249 セレクタ文字列への値連結 `input[name="modType"][value="${type}"]` →
    `querySelectorAll` + `value` 比較へ変更（CSS セレクタ注入の余地を排除）

### 3. 再発防止（新規スクリプト）

- `scripts/check_xss.js`（node / 依存なし）
  1. `escapeHtml`/`escapeAttr`/`cssClass` の定義が `escape.js` の1箇所だけか
  2. `escape.js` が利用側スクリプトより先に読み込まれているか（HTML の script 順）
  3. `class`/`id`/`href`/`title`/`style` 等の属性値内 `${...}` が
     「無害化関数で包まれている／数値確定／リテラル三項／無害化済みローカル変数」か
  4. 生成 HTML にインラインイベントハンドラ・`javascript:` が無いか
- `scripts/verify.sh` の静的検証に組み込み（i18n チェックの直後、`[ OK ] frontend XSS ...`）。

### 4. 検証

- `node --check`（escape.js / app.js / modules.js / check_xss.js）= 0
- `node scripts/check_xss.js web/static` = OK（4 ファイルすべて安全）
- 負例テスト: `app.js` に `class="x ${pluginValue}"` を追記した一時コピーで **exit 1** を確認（検出力あり）
- `escape.js` の単体アサーション 8 件 OK（`cssClass` が引用符・タグを除去、`null`→`''`、数値→文字列）
- 稼働中ダッシュボードから配信される実ファイルを確認: `GET /escape.js` に `cssClass` 4 箇所、
  `GET /app.js` に `cssClass(` 8 箇所 → **再起動不要で反映済み**（静的配信は `./web/static` をディスクから読むため）

---

## A-2 FIM 権限警告ノイズ削減（実施済み・.so は検査済み未配備）

### 1. 現状把握（実測）

- 監視対象（既定）: `/etc/passwd` `/etc/group` `/etc/hosts` `/etc/crontab` `/etc/ssh/sshd_config`
  `/etc/ld.so.preload` `/etc/shadow` `/etc/sudoers` `/root/.ssh/authorized_keys`
- 非 root（`user`, uid=1000）のため `/etc/shadow` `/etc/sudoers` `/root/.ssh/authorized_keys` が読めない。
- 配備済み `.so`（18:14）には既に「権限不足は INFO へ降格」「24時間スロットル」「最終通知時刻を
  FIM 状態へ永続化」が含まれており、稼働ログ `logs/kizuna-security.log` の該当は **3 件のみ**
  （18:15 に3ファイル分の INFO が1回）。従来の WARN 連投は解消済み。
  - 状態ファイル `logs/kizuna-security-fim.json` の `unreadable` に最終通知時刻が保存され、
    再起動でも繰り返さない（`TestFIMUnreadableAlertIsRateLimitedAcrossRestarts`）。

### 2. 追加実装（ノイズをさらに 1/3 に）

- `plugins/Kizuna-Security/plugin/fim.go`
  - 同一チェック内の読取不能を `pendingPerm` / `pendingOther` に集約し、**1 イベントにまとめて通知**。
  - レベル: 権限不足のみ → `info`、権限以外（削除・symlink 差し替え等）が混在 → `warning`。
  - `Source` は対象をソートして `,` 連結（1 件時は従来どおりパス単体）。
  - import に `sort` / `strings` を追加。
- `plugins/Kizuna-Security/plugin/messages.go`
  - `integrity.unreadable.multi.perm.msg` / `integrity.unreadable.multi.msg`（ja/en）を追加。
- `plugins/Kizuna-Security/plugin/hardening_test.go`
  - `TestFIMUnreadableNotificationsAreAggregated` を追加（3 ファイル読取不能 → 通知は 1 件・`info`・
    メッセージに全パスを含む・2 回目は間隔内で再通知なし）。

### 3. 検証（Linux 実機でビルド・テスト）

- `gofmt -l .` = なし、`go vet ./...` = OK、`go test ./...` = ok（`TestFIM*` 全 PASS。新テスト含む）
- `GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o /tmp/kizuna_security.so.new .` = 成功（9,128,320 B）
- ※ Windows では本プラグインはビルド不可（`syscall.O_NOFOLLOW` 等の Linux 専用 API）→ 検証は実機で実施。

### 4. 追加のノイズ削減案（運用側・要判断／要再起動）

| 案 | 内容 | 副作用 | 必要な権限 |
|---|---|---|---|
| (a) 推奨 | 監視対象を実効的な集合へ絞る: `modules.json` の `integrity_files` に読める集合のみ指定 | `/etc/shadow` 等は**監視対象外**になる（改ざん検知できない） | なし（設定変更＋エージェント再起動） |
| (b) 推奨 | `sudo setcap cap_dac_read_search+ep /opt/kizuna-eye/bin/agent_linux` | 再ビルドのたびに再適用が必要。`/etc/shadow` を読めるようになり監視が有効化される（INFO も消える） | **sudo（対話必須）** |
| (c) | systemd unit 化して `AmbientCapabilities=CAP_DAC_READ_SEARCH` | A-4 と同時に実施するのが最も自然 | **sudo（対話必須）** |

現状の通知は「初回 1 回 + 24 時間ごと 1 回（3 ファイル分を 1 件に集約）」なので、
(a)(b)(c) のいずれも急ぎではない。**b/c は sudo が必要なため未実施**（対話セッションで実施）。

---

## A-3 G-3（悪意ある .so の検査時コード実行）の根本対策（提案）

### 1. 現状の正確な理解

- アップロード経路: `internal/api/plugins.go` → `os.CreateTemp(p.pluginsDir, name+".so.tmp-*")`（411 行）
  に保存 → `inspectPlugin()`（443 行）→ 成功時のみ確定パスへ移動。
  **「検査前に実行対象ディレクトリへ置かない」は既に実装済み**（検疫の第一段は達成済み）。
- 残る穴: `cmd/plugin-inspect/main.go:86` の `plugin.Open(soPath)` が **uid=1000 権限で .so の
  `init()` を実行**する。Go プラグインは `plugin.Open` 時点で任意コードが走るため、悪意ある .so を
  アップロードすると「検査」という口実で任意コードが動く（同じホスト・同じユーザー・ネットワーク到達可）。
  `plugin.Open` の利用箇所は 2 箇所のみ（`cmd/plugin-inspect/main.go:86`＝検査、
  `cmd/agent/main.go:426`＝インストール済みプラグイン読込）。

### 2. 実機で確認した前提（2026-10-03 実測）

| 項目 | 結果 |
|---|---|
| `bwrap` / `unshare` / `setpriv` / `systemd-run` / `nsenter` | すべて `/usr/bin` にあり（追加導入は不要） |
| `sudo -n`（非対話） | **不可**（`interactive authentication is required`）→ sudo を要する対策は対話セッションで |
| systemd user インスタンス | `running`（`systemd-run --user` が使える） |
| kizuna 用 systemd unit | **無し**。agent は `user` が直接起動（`agent_linux -config ... -modules ...`） |
| bwrap 隔離の実測 | `--unshare-all` で `ip -o a` は **lo のみ**（外部ネットワーク遮断）。その中で `plugin-inspect` は正常に JSON を返した |

### 3. 3案の比較

| 案 | 実装コスト | 効果 | 副作用 | sudo |
|---|---|---|---|---|
| 署名検証（Ed25519） | 中（メタ＋検証コード＋鍵管理） | ◎ 検査**前**に「実行してよい .so か」を判定できる（本来の根治） | プラグイン更新フローに署名が必須。未署名は拒否 | 不要 |
| 分離実行（bwrap） | 小（引数組み立てのみ） | ○ 検査時の任意コードをサンドボックスへ封じる（ネットワーク・ホーム・書込不可） | 起動時に外部接続する実装だと inspect が失敗し、fail-closed で拒否される | 不要 |
| 検疫ディレクトリ | 極小 | △（第一段は実装済み。0700 の専用 dir で露出をさらに限定） | なし（同一 FS 内 `rename` なので原子的） | 不要 |

### 4. 推奨（組み合わせ・優先順）

1. **検疫ディレクトリの厳格化**（極小・即時）
   `pluginsDir/.quarantine`（`0700`）を用意し、`CreateTemp` の第 1 引数をそこに変更。検査成功後に
   `os.Rename(quarantine/x.tmp, pluginsDir/x.so)`（同一 FS＝原子的）。
2. **bwrap による分離実行**（小・本命の緩和）
   `inspectPlugin` の `exec.CommandContext` を次の形に置き換える（bwrap 不在/失敗時は**拒否**＝fail-closed）:

   ```
   /usr/bin/bwrap --unshare-all --die-with-parent \
       --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp \
       -- <plugin-inspect> --so <quarantine/xxx.so.tmp>
   ```

   - `--unshare-all` = network/pid/ipc/uts/user/cgroup を分離（実測で外部 IF 消失を確認）
   - 既存の timeout（`exec.CommandContext`）に加え `--die-with-parent` で親死時に確実に終了
   - 設定キーで切替可能にする（既定 `bwrap`、`off` は従来動作）。bwrap 不在で `off` 以外なら拒否。
3. **（Phase 2）Ed25519 署名必須化**（中・抜本対策）
   - ビルド時に `.so.sig` を生成（管理鍵はオフホスト保管）。
   - ダッシュボードは「署名検証 → 検疫 → 検査 → 配置」、**agent は `plugin.Open` の前に署名検証**
     （agent 側で弾ければ、たとえ検査をすり抜けても実行されない）。
   - 設定 `plugins.require_signature = true` のとき、未署名・署名不一致は拒否（fail-closed）。

### 5. sudo 設定

- 上記 1〜3 はいずれも **sudo 不要**（bwrap は非特権で動作。実測済み）。
- さらに強固にする場合のみ、検査を systemd 一時サービス化して
  `systemd-run --user --pipe --wait --collect -p MemoryMax=512M -p CPUQuota=50% -p ProtectHome=read-only ...`
  を使う（これも sudo 不要）。

---

## A-4 agent 専用ユーザー化（設計提案）

### 1. 目的と現状のギャップ

- 現状: agent は `user`(uid=1000) で動作。FIM 状態・チェーン鍵・各式状態ファイルは `user` 所有。
- したがって **uid=1000 が奪われると FIM ベースライン/チェーン鍵/アラート状態を書き換えられ、
  「検知はできるが防止はできない」**（改ざん検知の信頼性が運用アカウントに依存）。
- 専用ユーザーへ分離すれば、運用アカウント侵害時でも検知系の状態・鍵は改変できない。

### 2. 設計

- ユーザー: `kizuna-agent`（system / nologin / home `/var/lib/kizuna-eye`）
- 状態ディレクトリ: `/var/lib/kizuna-eye/{state,keys,logs}`
  - 移動対象: `kizuna-security-fim.json` `-cron.json` `-ports.json` `-suid.json` `-logins.json`
    `-logstate.json` `-alertstate.json` `chain.key`（現: `~/.kizuna-eye/data/keys/chain.key`）
- 起動: systemd unit `/etc/systemd/system/kizuna-agent.service`
  ```ini
  [Unit]
  Description=Kizuna-Eye Agent
  After=network-online.target
  [Service]
  User=kizuna-agent
  Group=kizuna-agent
  AmbientCapabilities=CAP_DAC_READ_SEARCH
  CapabilityBoundingSet=CAP_DAC_READ_SEARCH
  NoNewPrivileges=yes
  ProtectSystem=strict
  ProtectHome=read-only
  PrivateTmp=yes
  ReadWritePaths=/var/lib/kizuna-eye /samba/share/Kizuna-Eye/logs /samba/share/CD
  ExecStart=/opt/kizuna-eye/bin/agent_linux -config /var/lib/kizuna-eye/state/agent_config.json -modules /var/lib/kizuna-eye/state/modules.json
  Restart=on-failure
  [Install]
  WantedBy=multi-user.target
  ```
  - `AmbientCapabilities=CAP_DAC_READ_SEARCH` により **A-2 の (b)(c) も同時に解決**
    （`/etc/shadow` を読めるようになり、権限 INFO も消える）。
- ダッシュボードは従来どおり `user`（閲覧・設定は非特権のまま）。ログ閲覧（`/api/logs`）は
  `/samba/share/Kizuna-Eye/logs` を `kizuna-eye` グループ経由で読めるようにする（0640 + グループ）。

### 3. 移行手順（対話 sudo）

1. バックアップ:
   `tar czf ~/kizuna-state-backup-$(date +%Y%m%d_%H%M).tgz /samba/share/Kizuna-Eye/logs/kizuna-security-* ~/.kizuna-eye/data`
2. `sudo useradd --system --home /var/lib/kizuna-eye --create-home --shell /usr/sbin/nologin kizuna-agent`
3. `sudo install -d -o kizuna-agent -g kizuna-agent -m 0700 /var/lib/kizuna-eye/{state,keys,logs}`

4. `sudo cp -a <現行の状態ファイルと chain.key> /var/lib/kizuna-eye/... && sudo chown -R kizuna-agent:kizuna-agent /var/lib/kizuna-eye`
5. unit 設置 → `sudo systemctl daemon-reload && sudo systemctl enable --now kizuna-agent`
6. 旧 agent（手動起動プロセス）を停止: `kill <PID>`
7. 確認: `systemctl status kizuna-agent` / `GET /api/status` / FIM の権限 INFO が消えること /
   ダッシュボードと Discord にアラートが届くこと / backup プラグインが `/samba/share/CD` へ書けること

### 4. ロールバック手順

1. `sudo systemctl disable --now kizuna-agent`
2. バックアップから状態を戻し `sudo chown user:user`（元の所有者へ）
3. 従来どおり `user` で起動（`/opt/kizuna-eye/bin/agent_linux -config ... -modules ...`）
4. `/api/status` と FIM ログで復旧確認

### 5. 副作用・要確認事項

- backup プラグインは agent 内で動作するため `kizuna-agent` に `/samba/share/CD` への書込権が必要
  （現状 `0777` + ACL のため可。Samba の ACL 正規化に注意）。

---

## 追記（2026-10-04）: A-3 実装完了と検証結果

### 1. 実装（前回からの差分）

- `internal/pluginsig/`（新規）: Ed25519 の鍵/署名の生成・読み書き・検証。
  - `EncodePrivateKey` `EncodePublicKey` `LoadPrivateKey` `LoadPublicKey`
  - `SignFile`（`.so` → `<x>.so.sig`、0600）、`VerifyFile`（`.sig` 不在は `ErrNoSignature`）、
    `VerifyBytes`（アップロード時に multipart で届いた署名の検証）
  - 署名は base64 / hex と `#` コメント行・空白を受理。公開鍵は PKIX PEM / hex / base64。
- `cmd/plugin-sign/`（新規）: `-gen-key` `-sign` `-sign-all` `-verify` `-print-public-key` `-force`。
- `internal/api/plugins.go`:
  - `plugins_dir/.quarantine`（0700）へ `CreateTemp` → **署名検証 → bwrap 隔離検査 → 同一 FS 内 `rename`** の順に固定。
  - `readUploadedSignature`（`signature` ファイル or `signature_text`、上限 8 KiB）。
  - `inspectCommand`（bwrap。`plugins.inspect_isolation` が `bwrap` で未導入なら fail-closed）。
  - 削除時に `.sig` を必ず削除。
- `cmd/agent/main.go`: `plugin.Open` の**前**に署名検証（読み込み時に `init()` が走るため）。
- `pkg/config/config.go`: `PluginSecurityConfig`（`require_signature` / `public_key_file` /
  `inspect_isolation` / `bwrap_path`）。
- フロント: `modules.html` のファイル入力に `accept=".so,.sig" multiple`、`modules.js` で
  `.so` と `.sig` を分離し `signature` を FormData へ追加、i18n（日英）に選択ガイドを追加。
- 配布系: `build.sh`・`install.sh`・`update.sh`・`safe_update.sh`・`scripts/build.sh`・
  `scripts/dev.sh`・`scripts/verify.sh` に `plugin-sign` を組み込み、
  `install.sh` の必須パッケージに `bubblewrap` を追加。`scripts/verify.sh --full` は
  配置済み `.so` と `.sig` を公開鍵で検証する（`KIZUNA_PLUGIN_PUBKEY` でパス変更可）。
- `go.work` / `go.work.sum` は開発者ローカルとして `.gitignore` へ追加
  （プラグイン側 `go.mod` に `replace Kizuna-Eye => ../../..` があるため必須ではない。
  コミットすると CI がプラグイン module まで評価対象に変わるため）。

### 2. 検証結果（2026-10-04 実施）

| 項目 | 結果 |
|---|---|
| `go build ./...`（Windows / go1.27.1） | 成功（exit 0） |
| `go test ./internal/... ./pkg/...` | 全パッケージ ok（`internal/pluginsig` の 0600 アサートのみ Windows では表現不能なため `runtime.GOOS` ガードを追加。Linux 側で確認済み） |
| `gofmt -l cmd pkg internal` | 出力なし（整形済み） |
| `node --check web/static/modules.js` / `i18n.js` | 成功（exit 0） |
| Linux（go1.26.0 + GOTOOLCHAIN=auto）`go test ./internal/pluginsig/ ./internal/api/ ./pkg/config/` | 全 ok |
| プラグイン単体ビルド（`GOWORK=off go build -buildmode=plugin`） | 成功 |

`cmd/plugin-sign` のラウンドトリップ（Linux 実機、`/tmp/a3rt`）:

```
GENKEY_OK  priv_mode=600 pub_mode=644
SIGN_OK    sig_mode=600
VERIFY_OK
TAMPER_EXIT=1        # .so を 1 バイト追記 → 検証失敗（exit 1）
NOSIG_EXIT=1         # .sig 削除 → ErrNoSignature（exit 1）
WRONGKEY_EXIT=1      # 別の鍵で検証 → 失敗（exit 1）
SIGNALL_OK sigs=2    # ディレクトリ直下の .so を一括署名
SIGNALL_IDEMPOTENT=yes  # 既存署名は -force なしで上書きしない
PRINTPUB_OK          # -print-public-key が生成時の公開鍵と一致
```

### 3. 残作業（要 sudo / 対話）

以下は自動実行できないため、対話 sudo で実施する（コマンドは `systemd/` 配下のスクリプトに収録）。

1. `sudo ./systemd/install-services.sh <run-user>` … unit 設置 + `daemon-reload` + enable
2. `sudo ./systemd/migrate-agent-user.sh <run-user>` … 状態ファイル/鍵の `/var/lib/kizuna-eye` 移行
3. `sudo ./systemd/setup-coredump.sh` … `core_pattern` 設定（`systemd/60-kizuna-core.conf`）
4. 鍵の配布: `plugin-sign -gen-key -key-dir <オフホスト>` → 公開鍵を `/etc/kizuna-eye/plugin_signing.pub` へ
   （秘密鍵は共有に置かない）
5. 既存 `.so` への署名: `plugin-sign -sign-all /opt/kizuna-eye/bin/plugins -private-key <秘密鍵>`
6. `bubblewrap` の導入（Ubuntu 24.04 では AppArmor の unprivileged userns 制限に注意。
   必要なら `kernel.apparmor_restrict_unprivileged_userns=0`）
7. クラッシュ相関の確認: `./scripts/sigsegv_report.sh`
- ダッシュボードのログ閲覧は agent 側ログの読取権限に依存（グループ共有で解決）。
- 鍵ローテーション手順・退避鍵ディレクトリ（`~/.kizuna-eye/keys/retired/`）のパス変更が必要。
- `.so` 配置先 `/opt/kizuna-eye/bin/plugins` は root 所有・0755 が望ましい（現状 `user` 所有）。
- 必要な sudo 操作は**対話セッションでのみ**実施（`sudo -n` は不可を実測）。サービス再起動を伴うため、
  実施前に作業ウィンドウを確保する。

---

## 重要: 既存の再発性クラッシュ（今回の作業中に発見・未解決）

### 症状

- agent / dashboard が `SIGSEGV` で落ちる。ログ上の signature:

  ```
  runtime: g 1: unexpected return pc for runtime.sigpanic called from 0x77acd19867b3
  ...
  ^ <encoding/json/v2.makeStructArshaler.func2+0xdb3>
  ^ <runtime.mallocgc+0x76>
  ```

  - 返り先 `0x77ac...` は Go のコード領域外（libc 相当）＝**不正な関数ポインタ／ポインタ破壊**。
  - `encoding/json/v2` は Go 1.27 の `encoding/json` 内部実装（本リポジトリは `encoding/json` を使用、
    json/v2 の直接 import も `GOEXPERIMENT` も無し）＝**標準ライブラリの JSON 経路で表面化**している。

### 発生状況（実測カウント）

| ログ | `unexpected return pc` の回数 |
|---|---|
| `logs/agent.log`（現行） | 3（直近: 2026-10-03 18:36 以降） |
| `logs/agent.log.1` | 31 |
| `logs/agent.log.2` | 6 |
| `logs/agent.log.3` | 1 |
| `logs/dashboard.log` | 2 |

- 直近の例: 18:15〜18:36 稼働後に落ち、02:15 の再配備時点で**旧 agent は既に死亡**していた
  （`pgrep` 不該当）。今回の A-1/A-2 変更とは無関係（`agent.log.3`＝10/1 にも記録あり）。
- 影響: agent が落ちると FIM / SSH ログイン / ポート / SUID / cron 監視が**静かに停止**し、
  通知も届かない（自動再起動の仕組みが無い。systemd 未使用・手動起動のため）。

### 推奨する次の対応（優先度: A-1〜A-4 より高い）

1. **supervisor 化（A-4 と同時作業）**: systemd unit に `Restart=on-failure` / `RestartSec=5` を入れ、
   監視断を最小化（A-4 の unit 案に既に含めた）。
2. **コアダンプ採取**（要 sudo）: `ulimit -c unlimited`、
   `/proc/sys/kernel/core_pattern` を見直し、`GOTRACEBACK=crash` で起動して再現時の情報を確保。
3. **発生時刻の相関分析**: 直前の「セキュリティイベント送信」との関係（JSON 直列化の直後に落ちる傾向）を
   時系列で確認。
4. **再現手順の確立**: 同一イベント（SSH ログイン急増など）を流し、`json.Marshal` 経路で再現するか確認。
   再現すれば Go 1.27.1 の JSON 実装起因の可能性を切り分けられる（ツールチェーンの切替で比較）。
5. 暫定監視: ユーザー cron での死活監視＋再起動（sudo 不要）。

> 今回の再起動後: 新 agent（PID 545918）は `Agent 認証済み接続`（02:15:41）を取り、
> Kizuna-Security も `configured` を記録。権限不足の再通知は無く（`.so` のスロットル＋永続化が有効）、
> 集約版 `.so`（`unreadable.multi` メッセージを含む）が稼働中。


- `web/static/escape.js`（`cssClass` 追加）
- `web/static/app.js` / `web/static/modules.js`（属性値の無害化統一・セレクタ連結の排除）
- `scripts/check_xss.js`（新規・静的検査）/ `scripts/verify.sh`（検査組み込み）
- `plugins/Kizuna-Security/plugin/fim.go`（読取不能通知の集約）
- `plugins/Kizuna-Security/plugin/messages.go`（集約メッセージ ja/en）
- `plugins/Kizuna-Security/plugin/hardening_test.go`（集約テスト）
- 本ドキュメント（A-3 / A-4 設計と A-1 / A-2 実施記録）

---

## 追加記録: リードの push（未完了・要ユーザー対応）

本文書のコミットを除き、`master` は `origin/master`（`4713e85`）から
**63 コミット先行 / 0 遅れ**（fast-forward 可能）。ただし push は GitHub 側の権限で拒否された。

```
remote: Permission to ExiServerTeam/Kizuna-Eye_forEN.git denied to sy815twty-spec.
fatal: unable to access 'https://github.com/ExiServerTeam/Kizuna-Eye_forEN.git/':
       The requested URL returned error: 403
```

- 読み取り（`git ls-remote` / fetch）は成功するため、認証情報自体（Git Credential Manager）は有効。
- 認証済みアカウント `sy815twty-spec` が `ExiServerTeam/Kizuna-Eye_forEN` の Write 権限を持っていない。
- SSH 経路も不可: `~/.ssh/id_ed25519` は存在するが GitHub に未登録
  （`git@github.com: Permission denied (publickey)`）。
- `sy815twty-spec/Kizuna-Eye_forEN` という fork は存在しない（`Repository not found`）。

### 解消手順（いずれか）

1. Org オーナーに `sy815twty-spec`（sy815twty@gmail.com）を Write 以上で招待してもらい、
   その後に `git push origin master` を再実行する（他に変更は不要）。
2. 当該リポジトリへの書き込み権限を持つアカウントのトークンで再認証してから `git push origin master`。
3. 権限を持つ別リモートへ退避 push:
   `git remote add backup <URL>; git push backup master`

### 作業保全（push できない間の受け渡し用）

- `tmp/lead_a3a4.bundle`（基点 `4713e85` / 収録 `refs/heads/master`）
  - 受け手での取り込み: `git pull <bundle> master`
    （基点を持たない場合は `git fetch <bundle> master:refs/heads/<branch>`）。
  - 自己検証: `git bundle verify tmp/lead_a3a4.bundle` が `is okay` を返す。
    サイズと SHA256 は転送時に別途連絡する（本ノートへの追記でバンドルの内容も
    1 コミット分だけ変化し得るため、固定値をここには書かない）。
- `tmp/` は `.gitignore` 対象なので push 内容には含まれない。

## 残タスク（要 sudo / 判断）

| # | 内容 | 状態 |
|---|---|---|
| 2 | SMB 共有の絞り込み（`valid users`、`sudo smbpasswd -a <user>`） | 手順記録済み・実機適用待ち |
| 3 | 秘密（`*_config.json` / `chain.key` / `modules.json`）の共有外移設 | 同上 |
| 4 | 署名鍵のローテーション（旧鍵は `~/.kizuna-eye/keys/retired/` へ退避） | 同上 |
| 5 | プラグインリポジトリの取り込み方針（Kizuna-Security を同梱継続か別リポジトリ化か） | 未決 |
| 6 | `.gitignore` の重複整理と `install.sh` の整合確認 | 完了（`plugins/**/*.so` 削除 + `go.work`/`go.work.sum` 追加） |
| 7 | `scripts/sigsegv_report.sh` によるクラッシュ相関分析 | 完了（sudo 不要で実行可） |

sudo が必要な具体コマンドは本ドキュメント「### 3. 残作業（要 sudo / 対話）」を参照。
