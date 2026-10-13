# Kizuna-Eye プロジェクト ロードマップ / 課題管理

> 最終更新: 2026-10-05

## 0. セキュリティ6層の現状（サマリ）

セキュリティは層で考える。1箇所の完璧さではなく、突破すべき壁を増やす。
評価: A(強い) / B(良好) / C(要強化)。

| 層 | 現状評価 | 実装済み | 未実装 / 要強化 |
|---|---|---|---|
| 1. ネットワーク | B | リバースプロキシ前提、agent_token、CSWSH/CSRF 対策、レート制限 | `listen_addr` 既定が全IF、`secure_cookies` 既定 false → セキュアデフォルト化 |
| 2. 認証 | C | ロール/ bcrypt / セッション / 最終管理者保護 | `auth.enabled` 既定 false、`public_viewer` 既定 true、`agent_token` 既定 空 → セキュアデフォルト化 |
| 3. プラグイン | B- | 別プロセス検査、Ed25519 署名検証（任意）、bwrap 分離（任意） | `require_signature` 既定 false → 既定 true 化、共有型の最小化 |
| 4. ログ | B+ | HMAC チェーン＋定期検証、alert_history 整合性、inode 追跡、FIM | リモート転送、alert_history への HMAC 署名 |
| 5. 権限 | B | sudo ヘルパー絶対パス化、sudoers 引数固定（M-3）、**agent 専用ユーザー化（H-1）完了**（kizuna-eye + systemd ProtectSystem/NoNewPrivileges/AmbientCapabilities） | systemd 保護の他ユニットへの横展開、HMAC 鍵ローテーション自動化 |
| 6. 運用 | C+ | CONTRIBUTING 受入範囲、CI(go test/-race) | SECURITY.md 拡充(済)、govulncheck CI(済)、ROADMAP(本書)、脆弱性開示ポリシー |

## 0.1 実装済み / 未実装 / やらないこと

### 実装済み（主要）
- ログ i18n: agent.log / dashboard.log を JSON Lines（message + message_en）化。
- frontend XSS ガード（escape.js 一元化 + check_xss.js）。
- FIM ディレクトリ監視（inotify）＋深さ/監視数/サイズ上限。
- プラグイン署名検証（Ed25519）、plugin-inspect の bwrap 分離（任意）。
- ログローテーションの inode 追跡（monitor.go）。
- alert_history の最終行ハッシュによる自己圧縮と改ざんの区別。
- agent 専用ユーザー移行スクリプト（systemd/migrate-agent-user.sh）。
- **H-1 agent 専用ユーザー化（権限分離）完了**（2026-10-05）。agent は専用ユーザー `kizuna-eye`、unit は `kizuna-eye-agent.service`、dashboard は `kizuna-dashboard.service`。`install.sh` だけで systemd 登録まで完結。`uninstall.sh` で撤去可能。

### 未実装 / 要強化（優先度順）
- P1: セキュアデフォルト化（層1/2/3）、ログのリモート転送、alert_history の HMAC 署名。
- P2: 共有型の最小化、HMAC 鍵の自動ローテーション。
- P3: E2E 自動化、Docker 再現手順。
- 完了: 作業記録のサニタイズ（内部記録を資料フォルダへ退避して repo から削除）。

### やらないこと（現時点）
- 大規模な新機能（メンテナのレビュー体制が追いつかないため）。
- `*.sh`（build/release/update）の無協議な変更。
- 重いサンドボックス（gVisor 等）の常時適用（低スペック環境と矛盾）。
  プラグイン検査の分離は bwrap を任意適用とし、運用ルールで補う。
- 完全な改ざん防止の保証（同一ユーザー権限では不可能。H-1 で権限分離済みだが、alert_history 鍵は書き手=dashboard のため user 権限奪取には完全防御でない）。

---

> 以降は既存の課題管理（2026-09-28 時点の詳細）。

> 最終更新: 2026-09-28
> 本書は「現状の正確なステータス」と「優先度」を付けて課題を管理する。
> ステータス: ✅解決済 / 🟡一部対応 / ⬜未対応

---

## 優先度の目安

- **P0 (最優先)**: セキュリティ・データ損失・公開ブロッカー
- **P1 (高)**: 品質保証・再現性・持続可能性に直結
- **P2 (中)**: 使い勝手・収益化・ドキュメント整備
- **P3 (低)**: あると良い改善

---

## 1. ドキュメントと実装の乖離 → 🟡 一部対応

**現状**
- Disk S.M.A.R.T は `smartctl` 未検出環境では取得しない（`pkg/module/system.go`）。
  README では「optional」と明記済み。
- 「完了」の定義が記録ごとに揺れている。

**対応**
- ✅ README / README.ja.md の要件に `smartctl (optional)` を明記済み。
- ⬜ **完了定義の統一**: 下記「完了の定義」を全記録で用いる。

**完了の定義（統一案）**
1. `go build ./...` が成功する
2. `go vet ./...` がクリーン
3. `go test ./...` が全パス
4. 追加機能にはテストを添付（カバレッジ低下を許容しない）
5. README / CHANGELOG を更新
6. セキュリティ関連は `scripts/verify.sh --full`（Linux）で確認

---

## 2. バージョン表記の揺れ → 🟡 一部対応

**現状**
- `internal/api/version.go` の `Version` と CHANGELOG の版が一致している。
- タグと Phase の対応表が外部から見えにくい。

**対応**
- ✅ リポジトリ直下の `VERSION` を**単一の真実源**とした。`Makefile` /
  `build.sh` / `scripts/build.sh` / `scripts/verify.sh` / `install.sh` /
  `update.sh` はすべて `VERSION` を読む（ハードコードを撤去）。
- ✅ `scripts/stamp_version.sh` で `VERSION` を `web/static` の
  `?v=`（キャッシュバスター）と `version-badge` / `const VERSION` に
  一括反映する。冪等（2回目は差分ゼロ）。
- ✅ 実行手順を確立: (1) `VERSION` を書き換える (2)
  `./scripts/stamp_version.sh` (3) `git diff` を確認してコミット。
- ⬜ リリース時に Git タグ `vX.Y.Z` を必ず打つ（`VERSION` と一致させる）。
- 注: `internal/api/version.go` の既定値は `go test` 用のフォールバック。
  実バイナリには ldflags で `VERSION` の値が注入される。

---

## 3. テストと検証の属人化 → 🟡 一部対応

**現状**
- `go test ./...` は CI で実行。
- CI は `go test -race ./...` を実行するよう更新済み（`.github/workflows/ci.yml`）。
- ブラウザ操作・SSH転送・cron 実行は手動依存。

**対応**
- ✅ CI に race 検出を追加済み。
- ✅ `scripts/verify.sh` に「gcc 無しなら race をスキップ」判定を追加済み。
- ✅ **通知の wire format をテストで固定**（`pkg/notify/payload_shape_test.go`）。
  Discord の embed 構造・`allowed_mentions.parse=[]`、LINE の
  form エンコードと Bearer を検証。Webhook 仕様変更やリファクタで
  静かに壊れるのを CI で検知する。LINE の API URL はテスト可能に変数化。
- ⬜ **E2E の自動化**: 下記を段階的に CI へ。
  - [ ] `/health` と `/api/*` のスモーク（`verify.sh --smoke` を CI に組込）
  - [ ] WebSocket 接続の自動テスト
  - [ ] プラグイン `.so` のロード検査（Linux）
- ⬜ **体調に依存しない品質保証**: CI を「必須ゲート」にし、ローカル検証は補助に位置づける。

---

## 4. PRO版の実装量と収益化 → ⬜ 未対応（別リポジトリ / 事業判断）

**現状**
- Kizuna-Backup は LITE（OSS）と PRO（有料構想）に分離。
- PRO の販売チャネル・価格・ライセンス管理は未決定。

**対応（提案 / 順序を厳守）**
- ⚠️ **PRO のコーディング着手前に、必ず次を決める**（順序を守る）:
  1. 販売チャネル（BOOTH / Gumroad / 自前 / GitHub Sponsors など）
  2. 価格（買い切り / サブスク / 寄付）
  3. ライセンス管理方式（オフライン検証 or アカウント）
- ⬜ **スコープを絞る**: PRO の機能（GPG / GFS / クラウド / 復元テスト / 直接通知）を
  MVP と Post-MVP に分割。
- ⚠️ 実装量は LITE の 3〜4 倍。健康上の制約を踏まえ、2〜3 週間の見積もりは
  非現実的。MVP を更に小さく切ること。
- ⬜ **収益モデルを1つに決める**: 買い切り / サブスク / スポンサー。
- ⬜ **ライセンス管理**: オフライン検証 or アカウント方式。
- 本件は Kizuna-Eye 本体の範囲外。Kizuna-Backup 側のロードマップで管理する。

---

## 5. プラグイン機構の運用負荷 → 🟡 一部対応

**現状**
- `pkg/module` / `pkg/status` 変更時は本体＋全 `.so` を同一ソースで再ビルドが必須。
- ホットリロードやバージョン互換の仕組みは未実装。

**対応**
- ✅ README / README.ja.md に「プラグイン再ビルドの運用注意」を明記済み。
- ⬜ **再ビルド自動化**: `scripts/rebuild-plugins.sh` で全プラグインを一括再ビルド。
- ⬜ **互換性チェック**: 起動時に `plugin.Open` 失敗を検知し、警告を明確化。

---

## 6. セキュリティ面の未整備 → ✅ ほぼ解決

**現状（2026-09-28 時点）**
- ✅ **認証・認可を実装済み**（`internal/auth`、14ファイル）。
  - admin / operator / viewer のロール、bcrypt、セッション Cookie。
  - 初回セットアップ、最後の管理者保護、IP単位レート制限。
- ✅ Agent の WebSocket トークン認証。
- ✅ CSWSH 対策（CheckOrigin 同一ホストのみ）。
- ✅ CSRF 対策（Origin 検証 + SameSite=Lax）。
- ✅ 認証無効時のプラグインアップロードを 403 で拒否。
- ✅ **プラグイン署名検証**（Ed25519、任意）。純正プラグインは install.sh / update.sh が署名。
- ✅ **install.sh / update.sh の権限設計**: `--systemd` で agent を専用ユーザー `kizuna-eye`（最小権限）へ移行。

**対応**
- ✅ プラグイン `.so` の署名検証（`plugins.require_signature` で有効化。install/update が再署名）。
- ✅ インストーラは systemd 保護（ProtectSystem / NoNewPrivileges / AmbientCapabilities）で最小権限動作。

---

## 7. 国際化対応の遅れ → 🟡 一部対応

**現状**
- `web/static/i18n.js` に ja / en の辞書あり。
- **整合性チェッカー（`scripts/check_i18n.js`）で完全一致を検証済み**
  （386 ja / 386 en キー、使用 301 キー、欠落ゼロ）。
- 一方、**Go 側のエラーメッセージは日本語のみ**。英語 UI に切り替えても
  サーバーが日本語エラーを返すため、英語圏ユーザーには不親切。
- 通知本文（Discord 等）も日本語のまま。

**対応**
- ✅ UI の ja/en 対応は完了（386キー一致、CI 相当のチェッカーで検証）。
- ⬜ **サーバーエラーの構造化**: `writeError` が返す文字列を機械可読な
  `code`（例 `auth.invalid_credentials`）に置き換え、フロントで i18n 翻訳
  する。全ハンドラ横断のため段階的に。
- ⬜ **通知本文の英語化**: テンプレート化を検討。
- ⬜ **サーバーログの英語化**: 運用ログは英語を推奨（現状は日本語混在）。

---

## 8. 開発体制の単一障害点 → ⬜ 未対応

**現状**
- 実装・テスト・ドキュメント・宣伝・運用を一人で担当。
- 健康上の制約あり。CONTRIBUTING.md は用意済みだが受入体制は未整備。

**対応**
- ✅ **受入範囲を CONTRIBUTING.md に明記**した（曖昧さを排除）。
  - 事前協議なしで受入: ドキュメント / 翻訳(i18n) / バグ報告。
  - Issue 先行: 機能追加・リファクタ・共有型(`pkg/module`/`pkg/status`)
    の変更（Go プラグインは全 `.so` 同時再ビルドが必要なため高影響）。
  - 現時点で受入れ不可: 大規模機能PR、`*.sh` の変更。
- ⬜ **引き継ぎ可能なドキュメント**: 本ロードマップ + 手順書を整備。
- ⬜ **Issue / PR テンプレート**を追加。
- ⬜ **「動けない日」前提の運用**: CI を必須ゲート化し、手動リリースを減らす。

---

## 9. 記録の肥大化と可読性 → ✅ 解決

**現状**
- 作業記録が大量に蓄積し全体像が掴みにくかった。

**対応**
- ✅ 本書 `docs/ROADMAP.md` を「現在の課題」の入口として作成。
- ✅ 内部記録（作業記録・計画書・プラグイン監査記録）を `Kizunaシリーズ　資料/`
  へ退避し、repo から削除。公開リポジトリは `PROCEDURE.md` / `ROADMAP.md` のみ。

---

## 10. 収益化とOSS公開のバランス → ⬜ 未対応

**現状**
- Kizuna-Eye は OSS（MIT）、Kizuna-Backup は PRO有料/LITE無料。
- 有料版の価値維持方針が未確定。

**対応（提案）**
- ⬜ PRO の価値を「サポート・アップデート・クラウド連携」に寄せる。
- ⬜ LITE と PRO の機能重複を管理するマトリクスを維持。
- 本件は Kizuna-Backup 側で管理。

---

## 11. ハードウェア依存と再現性 → 🟡 一部対応

**現状**
- 検証環境が特定サーバー（A4-4020 / i7-3770 / Samba共有）に依存。
- `GOWORK=off` / `CGO_ENABLED=1` / `golang.org/x/sys` の版統一など注意点多。

**対応**
- ✅ CI（ubuntu-latest）で再現可能なビルド・テストを実行。
- ✅ `go.mod` で依存版を固定。
- ⬜ **Docker / コンテナでの再現手順**を提供（任意）。
- ⬜ README に「前提ツールチェーン」を明記。

---

## 12. 長期的なメンテナンス計画の不在 → ⬜ 未対応

**現状**
- Go 版アップ、依存更新、セキュリティパッチの方針が未記載。

**対応（提案）**
- ⬜ **依存更新の定期チェック**（月1回 `go get -u` + テスト）。
- ⬜ **Go バージョンのサポート方針**（現行 + 1つ前）。
- ⬜ **セキュリティパッチの適用手順**を SECURITY.md に追記。
- ⬜ 持続可能な開発ペース（週次/月次）を決め、無理をしない。

---

## 付録: Phase ↔ バージョン対応（暫定）

| Phase | バージョン | 主な内容 |
|---|---|---|
| Phase 1-2 | v0.1.0 | 初期リリース、UI改善 |
| Phase 3-4 | v0.2.0 | CPU/温度/負荷/ネット、S.M.A.R.T |
| Phase 5 | v0.3.0 | CPUコア表示、スパイク制御 |
| Phase 7 | v0.4.0 | SNS通知、Agent死活監視、アラートエンジン |
| Phase 8-1 | v0.5.0 | LITE版プラグイン |
| Phase 8-4〜8-6 | v0.6.0 | cronスケジューラ、手動実行、プラグインアップロード |
| Phase 9-10 | v0.6.1 | 設定エディタ、i18n、認証、ログ、メトリクス履歴 |
| — | v0.7.x | セキュリティ強化、堅牢化（本ロードマップ） |

> 正確な対応は Git タグと CHANGELOG を正とする。

---

## 進め方の原則

1. **全部を一度にやらない**。P0 → P1 → P2 の順。
2. **体調を最優先**。動けない日は CI が品質を担保する。
3. **記録は入口を1つに**（本ファイル）。詳細は各記録へリンク。
4. **完了の定義を守る**（本書 1 章）。
