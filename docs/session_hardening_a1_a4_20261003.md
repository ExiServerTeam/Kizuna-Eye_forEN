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
- ダッシュボードのログ閲覧は agent 側ログの読取権限に依存（グループ共有で解決）。
- 鍵ローテーション手順・退避鍵ディレクトリ（`~/.kizuna-eye/keys/retired/`）のパス変更が必要。
- `.so` 配置先 `/opt/kizuna-eye/bin/plugins` は root 所有・0755 が望ましい（現状 `user` 所有）。
- 必要な sudo 操作は**対話セッションでのみ**実施（`sudo -n` は不可を実測）。サービス再起動を伴うため、
  実施前に作業ウィンドウを確保する。

---

## 付記: このセッションで変更したファイル

- `web/static/escape.js`（`cssClass` 追加）
- `web/static/app.js` / `web/static/modules.js`（属性値の無害化統一・セレクタ連結の排除）
- `scripts/check_xss.js`（新規・静的検査）/ `scripts/verify.sh`（検査組み込み）
- `plugins/Kizuna-Security/plugin/fim.go`（読取不能通知の集約）
- `plugins/Kizuna-Security/plugin/messages.go`（集約メッセージ ja/en）
- `plugins/Kizuna-Security/plugin/hardening_test.go`（集約テスト）
- 本ドキュメント（A-3 / A-4 設計と A-1 / A-2 実施記録）


