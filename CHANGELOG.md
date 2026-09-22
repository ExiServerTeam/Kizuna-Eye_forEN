# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-09-22

### Added
- Phase 8-5: Dashboard からの手動バックアップ実行
- Phase 8-4: cron式スケジューラ（`github.com/robfig/cron/v3`）
- Phase 8-6: 完全自動プラグインアップロード機能
- `plugin-inspect` バイナリによる別プロセス検証
- `ConfigField` / `ConfigProvider` インターフェース
- `DisplayNameProvider` インターフェース
- 動的プラグインフォーム生成（`modules.js`）

### Changed
- `internal/api/plugin.go` → `internal/api/plugins.go` にリネーム
- プラグインの `.so` を別プロセスで検証する方式に変更
- `modules.json` の `plugin_path` を `/opt/kizuna-eye/bin/plugins/` に統一
- `AgentHub` インターフェースを追加（Dashboard から Agent へのコマンド送信）

### Fixed
- Agent の30分ごとの切断バグ（`ReadDeadline` リセット漏れ）
- `SetWriteDeadline` を5秒から30秒に延長
- `omitempty` による `processes` フィールドの消失

## [0.5.0] - 2026-09-17

### Added
- Phase 8-1: LITE版プラグイン（Go完全書き直し）
- JSON Lines形式のログ出力
- `rotationLocal` による世代管理
- `SHA256File` によるハッシュ計算

## [0.4.0] - 2026-09-15

### Added
- Phase 7: SNS通知（Discord / Telegram / LINE）
- Agent死活監視（`agent_timeout_sec`）
- アラート判定エンジン（`pkg/alert/engine.go`）

## [0.3.0] - 2026-09-10

### Added
- Phase 5: CPUコアグリッド表示
- 空き容量バッジ
- アラートPulseアニメーション
- スパイク制御（`holdMs` / `cooldownMs`）

## [0.2.0] - 2026-09-05

### Added
- Phase 3: CPU型番・温度・Load Average・ネットワークIO収集
- Phase 4: ディスクS.M.A.R.T・ネットワーク速度

## [0.1.0] - 2026-09-01

### Added
- Phase 1: ライトモード視認性改善
- Phase 2: 単位統一・空状態CTA・トグルUI統一
- 初回リリース