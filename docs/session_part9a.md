
---

# Kizuna-Eye プロジェクト 作業記録 第16部

**セッション日付**: 2026年10月3日（金）続き8
**作業内容**: 脆弱性 V1-A / V2-B / V3 の修正実装

---

## 第93章: 発見した脆弱性（再掲）

- V1（高）: ハッシュチェーンに鍵がない。改ざん権限でチェーン再計算→VerifyChain偽装通過。起動時1回のみ検証。
- V2（中）: security.log / alert_history.jsonl が自己監視されていない。
- V3（要確認）: ヘルパースクリプトの PATH 依存。

---

## 第94章: V3（ヘルパーの絶対パス化）

basename/cut/sha256sum/printf を絶対パス化（/usr/bin/...）。which で確認済み。ファイルは root 所有のため管理者が sudo で設置。

結果: 未実行（管理者の設置待ち）。

---

## 第95章: V1-A（HMAC + 定期検証）

### 95-1. 実装

logger.go:
- chainHashKeyed(key, prev, entry) 追加。key あり→HMAC-SHA256、なし→従来の SHA-256。
- NewFileLoggerKeyed(path, keyPath) 追加。鍵は loadOrCreateChainKey で生成/読込（32B, 0600）。
- VerifyChainKeyed(path, key) 追加。
- 既存ログが検証不合格なら .legacy-<ts> へ退避し新チェーン開始。
- 後方互換のため鍵なし関数も残す。

integrity.go（新規）: runIntegrityChecks() で security.log を VerifyChainKeyed 検証、失敗時 critical。

plugin.go: Configure で NewFileLoggerKeyed 使用、Init で go p.integrityLoop() 開始。integrityLoop は起動30秒後に初回、以降 interval（既定300秒）ごと。

config.go: ChainKeyPath（既定 /samba/share/Kizuna-Eye/keys/chain.key）、IntegrityCheckInterval（既定300、30〜86400）。

### 95-2. 結果: 改ざん検知に成功

鍵生成: /samba/share/Kizuna-Eye/keys/chain.key（32B, 0600）。旧ログ移行: kizuna-security.log.legacy-20261003_031408。

改ざんテスト（2行目削除）:
{"title":"セキュリティログの改ざんを検知","level":"critical","message":"...ハッシュチェーンが壊れています: line 2: prev_hash mismatch"}

鍵なしの旧チェーンでは偽装通過できたが、HMAC 化で検知可能になった。
