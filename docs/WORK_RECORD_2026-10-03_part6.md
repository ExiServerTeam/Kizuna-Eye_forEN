
---

# Kizuna-Eye プロジェクト 作業記録 第13部

**セッション日付**: 2026年10月3日（金）続き5
**作業内容**: 回帰テスト（go test ./...）と修正

---

## 第81章: 回帰テスト

### 81-1. 初回結果

| 対象 | 結果 |
|---|---|
| Kizuna-Eye (go test ./...) | 全12パッケージ ok |
| Kizuna-Security プラグイン | 2件失敗 |

失敗:
--- FAIL: TestFIMNewWatchTargetSilent (newly added watch target should be silent, got: [... 監視対象をベースラインに追加 ...])
--- FAIL: TestSUIDDetectsNewFile (new SUID file should be critical: [])

### 81-2. 原因分析

#### ① TestSUIDDetectsNewFile → 実バグ（コード側）

suid.go Check() の hasBaseline := len(s.known) > 0。SUIDファイルが0件のシステムではベースラインが空 → hasBaseline が永久に false → 新規SUIDファイルを無言でベースラインに吸収。本番サーバーは既存SUIDファイルがあるためフェーズ3は成功したが、空ベースラインでは検知不能という実バグ。

#### ② TestFIMNewWatchTargetSilent → テストが古い（コード側は意図的）

fim.go は「監視対象に新規追加されたファイル」を意図的に警告する（攻撃者がベースラインへ紛れ込ませる抜け道を塞ぐ）。テストは silent を期待していたが、10/2のfim.go変更で古くなった。

### 81-3. 修正

1. suid.go: 専用 hasBaseline フラグを追加（record/load 時に true）。len(known)>0 を廃止。
2. security_extra_test.go: FIM新規監視対象の期待値を warning 1件に更新。
3. monitor_test.go: newTestMonitor で SSHLoginBaselinePath="" にし、共有ログインベースラインによるテスト順序依存を排除。

### 81-4. 修正後の結果

go test を3回連続実行、すべて成功:
run 1: ok  kizuna-security-plugin  0.248s
run 2: ok  kizuna-security-plugin  0.262s
run 3: ok  kizuna-security-plugin  0.252s

suid.go 修正を反映して kizuna_security.so を再ビルド・配備、Agent 再起動済み。

---

## 第82章: 既知の課題（追記）

### 82-1. ログローテーションの inode 追跡（改善候補・今回は未修正）

monitor.go の scanFile() は `if info.Size() < startOffset { startOffset = 0 }` でサイズ縮小のみローテーション検知する。inode 追跡がないため、ローテーション直後に新ファイルが旧offsetを超えて成長すると、その間の行を見逃す可能性がある（低確率）。

改善案: os.Stat で inode (Sys().(*syscall.Stat_t).Ino) を記録し、変化したら startOffset=0 にリセット。

優先度: 中。今回は未修正、後日対応。

### 82-2. テスト分離の一般化

newTestMonitor の共有状態問題は1件修正したが、他テストが同様の共有ファイル（相対パス）を使っていないか、今後点検する。

---

以上が、2026年10月3日セッション（続き5）の作業記録です。
