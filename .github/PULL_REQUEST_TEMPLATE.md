<!--
  Kizuna-Eye 変更提案テンプレート。CONTRIBUTING.md を先に読んでください。
  Kizuna-Eye pull request template. Please read CONTRIBUTING.md first.
-->

## 概要 / Summary

<!-- 何を、なぜ変えたか / What changed and why -->

## 種別 / Type

- [ ] バグ修正 / Bug fix
- [ ] ドキュメント / Docs
- [ ] 翻訳 (i18n) / Translation
- [ ] 機能追加 / Feature（Issue 先行が必要 / needs an issue first）
- [ ] リファクタ / Refactor（Issue 先行が必要 / needs an issue first）
- [ ] 共有型の変更 (pkg/module, pkg/status, pkg/fsutil) / Shared-type change（全 .so 再ビルド必須 / requires rebuilding every .so）

## 確認 / Checklist

- [ ] `gofmt -l cmd internal pkg` がクリーン / clean
- [ ] `go vet ./...` が通る / passes
- [ ] `go test ./...` が通る / passes
- [ ] 共有パッケージを変更した場合、agent と全プラグインを同一 combined go.work・`-trimpath` で再ビルドした / If shared packages changed, rebuilt the agent and every plugin under one combined go.work with -trimpath
- [ ] README / CHANGELOG を必要に応じて更新 / Updated README / CHANGELOG as needed

## 関連 Issue / Related issue

<!-- 例: Closes #12 -->
