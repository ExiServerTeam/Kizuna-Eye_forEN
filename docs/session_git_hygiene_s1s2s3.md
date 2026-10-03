# Git 汚染是正 (S-1 / S-2 / S-3)

- 日時: 2026-10-03
- 対象: `z:\Kizuna-Eye` (HEAD = c268fca, branch = master)
- 事前バックアップ: `C:\Users\sy815\Documents\Kizuna-Eye_snapshot_20261003_214110`
  (SHA-256 全ファイル突合で一致。不一致は稼働中 `logs/` 配下 5 件のみ)
- 作業方針: 指示された 10 コマンドを順に実行し、終了コードと出力のみ確認。ファイル内容の読取は行わない。

## 実行した手順と終了コード

| # | コマンド | exit |
|---|---|---|
| 1 | `git reset HEAD keys/chain.key` | 0 |
| 2 | `git reset HEAD _pentest_backup_20261003_005800/ _pentest_backup_20261003_005910/` | 0 |
| 3 | `git reset HEAD scripts/attack_hmac_forge.py scripts/ws_agent_attack.py scripts/d_poc_plugin.go` | 0 |
| 4 | `git reset HEAD cmd/dashboard/main.go.prepatch-src cmd/dashboard/main.go.prepatch-v4 pkg/alert/engine.go.prepatch-src` | 0 |
| 5 | `git reset HEAD web/static/app.js.prepatch-src web/static/i18n.js.prepatch-src web/static/style.css.prepatch-src` | 0 |
| 6 | `git reset HEAD modules.json.fimtest-bak` | 0 |
| 7 | `web/static/NDH6SA~M` の削除 | 0 |
| 8 | `.gitignore` への追記 | 0 |
| 9 | `git status --short` | 0 |
| 10 | `git diff --cached --stat` | 0 |

注: Windows PowerShell には `rm -f` / `printf` が無いため、step 7 は `Remove-Item -LiteralPath ... -Force`、
step 8 は `[System.IO.File]::AppendAllText(..., ASCII)` で等価実行した（追記内容は同一）。

## `.gitignore` 追記内容

```
keys/
_pentest_backup_*/
*.prepatch-*
*.fimtest-bak
*.swp
*.swo
*~
```

## 結果

- `keys/`、`_pentest_backup_*/`、`*.prepatch-*`、`*.fimtest-bak` は `git status` から消滅（ignore 化）。
- `scripts/attack_hmac_forge.py` / `ws_agent_attack.py` / `d_poc_plugin.go` は `??`（未追跡）へ移動。

## 未解決（要フォローアップ）

1. `web/static/NDH6SA~M` は index 上 `AD`（追加ステージ済み＋作業ツリー削除）。
   → `git rm --cached web/static/NDH6SA~M` が必要。
2. `scripts/` 配下の攻撃・パッチ系ファイルが stage 済み（`A`）のまま残存:
   `attack_chain_forge.py`、`patch_*.py`、`new_*_spoof.go.tpl`、`kizuna-cron-read.sh`、`stamp_version.sh` 等。
3. 履歴側の是正（過去コミットに残る `keys/chain.key` 等）は未実施。実施する場合は
   `git filter-repo` / BFG による履歴書き換えが必要（別タスク・要承認）。

# 追記: 残存2件の解消 (2026-10-03)

## 追加実行した手順と終了コード

| # | コマンド | exit |
|---|---|---|
| 1 | `git rm --cached web/static/NDH6SA~M` | 0 |
| 2 | `git reset HEAD scripts/patch_*.py` | 0 |
| 3 | `git reset HEAD scripts/new_*.tpl` | 0 |
| 4 | `git reset HEAD scripts/attack_chain_forge.py` | 0 |
| 5 | `git reset HEAD scripts/kizuna-cron-read.sh` | 0 |
| 6 | `git reset HEAD _pentest_backup_20261003_005800/ _pentest_backup_20261003_005910/` | 0 |
| 7 | `.gitignore` への追記 | 0 |
| 8 | `git rm --cached -r --ignore-unmatch _pentest_backup_*/` | 0 |
| 9 | `git status --short` | 0 |
| 10 | `git diff --cached --stat` | 0 |

注: PowerShell には `printf` / `2>/dev/null || true` が無いため、step 7 は
`[System.IO.File]::AppendAllText(..., ASCII)`、step 8 は `--ignore-unmatch` で等価実行した。

## `.gitignore` 追加追記内容

```
scripts/patch_*.py
scripts/new_*.tpl
scripts/attack_*.py
scripts/ws_agent_attack.py
scripts/d_poc_plugin.go
_pentest_backup_*/
```

## 結果

- `AD web/static/NDH6SA~M` は解消（index から除去、`git status` に出現せず）。
- `A scripts/patch_*.py` / `A scripts/new_*.tpl` / `A scripts/attack_chain_forge.py` は解消（ignore 済みで status に出現せず）。
- `?? scripts/attack_hmac_forge.py` / `ws_agent_attack.py` / `d_poc_plugin.go` は解消（ignore 済み）。
- `_pentest_backup_*/` は index から除去＋ignore 済みで status に出現せず。
- ステージ残: `A scripts/stamp_version.sh`、`M scripts/build.sh`、`M scripts/verify.sh`（今回のコマンド範囲外）。
- 未追跡残: `?? scripts/kizuna-cron-read.sh`（step 5 で unstage したが ignore パターン対象外）、
  `?? docs/session_git_hygiene_s1s2s3.md`（本作業記録）。
- ステージ総量: 125 files → 101 files、8237 insertions / 674 deletions → 5738 insertions / 674 deletions。