# セキュアデフォルト化 計画（層1・2・3）

> 最終更新: 2026-10-05
> ステータス: **計画のみ（実施は別途承認）**
>
> 方針: 「簡単」より「安全」を既定にする。ただし既存運用を壊さないよう、
> **新規デフォルト**と**既存設定の移行**を分けて扱う。

---

## 1. 変更対象と現在の既定

| 設定 | 現在の既定 | 提案する既定 | 層 | 実装箇所 |
|---|---|---|---|---|
| `listen_addr` | `:8080`（全IF） | `127.0.0.1:8080` | 1 | config.go LoadDashboardConfig |
| `secure_cookies` | `false` | `true`（auth 有効時） | 1 | AuthConfig.SecureCookies |
| `auth.enabled` | `false` | `true` | 2 | AuthConfig.Enabled |
| `public_viewer` | `true`（nil=既定） | `false`（nil=既定） | 2 | AuthConfig.IsPublicViewer |
| `agent_token` | 空 | 必須（空なら起動警告/拒否） | 2 | AuthConfig.AgentToken |
| `plugins.require_signature` | `false` | `true` | 3 | PluginSecurityConfig.WantSignature |

---

## 2. 影響範囲（既存運用への影響）

### 2.1 `listen_addr` → `127.0.0.1:8080`（層1）

- **影響**: リモート（別 PC・LAN）から `http://192.168.0.233:8080` に直接
  アクセスできなくなる。アクセスにはリバースプロキシ（Caddy/nginx）が必須。
- **既存運用**: 現在は全IFバインドで LAN から直接アクセスしている想定。
  デフォルトを変えると**既存ユーザーが即座にアクセス不能**になる。
- **注意**: `start.sh` / systemd はバインドアドレスを設定ファイルから読む。
  変更は設定ファイル経由なので、既存の `dashboard_config.json` に
  `listen_addr` が**明示されていれば影響なし**。明示が無い（既定依存の）
  場合のみ影響。

### 2.2 `secure_cookies` → `true`（層1）

- **影響**: HTTP でログインすると Cookie が送信されず、**ログインできない**。
  HTTPS（リバースプロキシ）が必須になる。
- **既存運用**: 現在 HTTP 直アクセスなら、認証有効時に締め出される。
- **注意**: `auth.enabled=false` なら Cookie を使わないため影響なし。
  「auth 有効時のみ true」を既定にすると安全。

### 2.3 `auth.enabled` → `true`（層2）

- **影響**: 初回アクセスで `/setup.html` にリダイレクトされ、管理者作成が必須。
  未認証ではダッシュボードが見えない（viewer もログイン必須）。
- **既存運用**: 認証なしで使っている場合、**設定なしでは使えなくなる**。
- **注意**: `users.json` が空の状態で有効化すると、誰でも最初の管理者に
  なれる（ネットワーク到達可能な場合）。**初回はローカル限定バインドと
  セット**で有効化するのが安全。

### 2.4 `public_viewer` → `false`（層2）

- **影響**: ゲストログイン（ログイン不要の閲覧）が無効になる。
- **既存運用**: `auth.enabled=true` かつ `public_viewer=true` で運用している
  場合、ゲスト閲覧が止まる。
- **注意**: これは `*bool`（nil=既定 true）。既定を false に変えると、
  **既存設定で `public_viewer` を明示していない全ユーザーが影響**を受ける。
  よって「既存設定は true のまま、新規のみ false」を選べるようにする
  （下記 3. 移行方針）。

### 2.5 `agent_token` 必須（層2）

- **影響**: token 未設定だと Agent が接続できない。
- **既存運用**: 空のままヒューリスティック検出で動かしている場合、**Agent が
  接続不能**になる。
- **注意**: これは「必須化」であり、既存運用には**最も影響が大きい**。
  段階的には「空なら起動時に強い警告」→「拒否」の2段階が安全。

### 2.6 `plugins.require_signature` → `true`（層3）

- **影響**: 未署名の `.so` をロードしなくなる。既存プラグインに `.sig` が
  無いと**プラグインが動かない**。
- **既存運用**: `kizuna_security.so` / `kizuna_backup_lite.so` には署名済み
  （`plugin-sign -sign`）。ただし `.sig` を配置し忘れていると拒否される
  （本セッションで実際に発生）。
- **注意**: 有効化前に全プラグインの再署名＋配置が必須。

---

## 3. 移行方針（既存運用を壊さない）

**原則**: 「新規インストール/新規設定」はセキュア既定、「既存設定」は現状維持。

1. **既定値の変更は「新規作成時のみ」に限定する**。
   - 設定ファイルが存在しない（初回起動）→ セキュア既定を書き出す。
   - 設定ファイルが存在する → 既存値を尊重（明示が無いキーのみ新既定を
     適用するかは、キーごとに判断）。
2. **`public_viewer` は特に慎重**: `*bool` の nil を false に変えると既存
   ユーザーが影響を受けるため、「設定エディタに警告を出し、移行を促す」
   方式を推奨。
3. **`agent_token` は2段階**: (a) 空なら起動時に「危険」警告をログ/画面に
   出す（実装済みの警告を強化）→ (b) 次期メジャーで必須化。
4. **`plugins.require_signature`**: 有効化前に `plugin-sign -sign-all` の
   手順を README/PROCEDURE に明記。build.sh への署名統合（層3 TODO）と同時に。
5. **`auth.enabled`**: 既定 true 化は「初回セットアップがローカル限定
   （`listen_addr=127.0.0.1`）で完結する」ことを前提にする。

---

## 4. 実装タスク（承認後）

- [ ] `LoadDashboardConfig`: 初回（ファイル無し）時の既定をセキュア側へ。
- [ ] `AuthConfig.IsPublicViewer`: nil の扱いを「新規のみ false」にできる
      仕組み（例: 設定にバージョンキー `config_version` を導入し、旧版は
      従来既定を維持）。
- [ ] `SecureCookies`: auth 有効時の既定 true 化＋HTTPS 必須の警告。
- [ ] `agent_token` 必須化の2段階（警告→拒否）。
- [ ] `require_signature` 既定 true 化＋署名手順の文書化。
- [ ] `config_version` による移行判定（破壊的変更の安全弁）。
- [ ] 設定エディタに「セキュア既定に合わせる」導線と警告。

---

## 5. リスクとロールバック

- デフォルト変更は**既存ユーザーのロックアウト**につながる最大リスク。
- 対策: `config_version` で旧設定を判別し、旧版は従来既定を維持。
  新規のみセキュア既定。移行は設定エディタの警告で誘導。
- ロールバック: 設定ファイルのバックアップ（`*.bak-<ts>`）から復元。

---

## 6. 推奨実施順序

1. `config_version` 導入（安全弁）
2. 層3（`require_signature`）＋署名手順の文書化・build.sh 統合
3. 層1（`listen_addr` / `secure_cookies`）
4. 層2（`public_viewer` → `auth.enabled` → `agent_token` の順で段階的）
5. 各段階で `verify.sh` と実機確認（要許可）
