# 共有型の最小化（pkg/module 分割）設計

> 最終更新: 2026-10-15

## 1. 目的

プラグイン `.so` の再ビルドを「契約を変えたときだけ」に減らす。今は
`pkg/module` が agent 内部専用の `system.go`（システム情報収集）を同居させて
おり、それが `pkg/status` を import するため、`pkg/status` を1行変えるだけで
全プラグインが `plugin was built with a different version of package` で
ロード不能になる。

## 2. メカニズム

Go プラグインは、ホストとプラグインが共有する全パッケージの build ID 一致を
要求する。プラグイン → `pkg/module` → `pkg/status`（system.go 経由）と
推移的に依存するため、`pkg/status` の変更が契約のハッシュを変えてしまう。

## 3. 目標構造

| パッケージ | 役割 | 依存 | 使う人 |
|---|---|---|---|
| `pkg/module` | **契約のみ**（IF・共有型・小さなヘルパー）。`pkg/status` を import しない | 標準ライブラリのみ | プラグイン＋ホスト |
| `internal/modulemgr` | ModuleManager 実装、実行ループ、panic 隔離 | `pkg/module` | ホストのみ |
| `internal/system` | SystemModule（システム収集）、`pkg/status` に依存 | `pkg/module`, `pkg/status` | ホストのみ |

これで `pkg/status`・`internal/*` を変えてもプラグイン再ビルドは不要。
再ビルドが要るのは `pkg/module`（契約）を変えたときだけになる。

## 4. 分割の境界

- `pkg/module` に残す: `Module` / `Logger` / `PluginModule` / `PluginConstructor`、
  全プロバイダ IF（Config / DisplayName / Tag / DashboardCard / WebAsset /
  BackupStatus / BackupRunner / SecurityEvent / SecurityEventRequeuer）、
  共有構造体（SecurityEvent / BackupStatus / BackupResult / ConfigField /
  DashboardCard）、`ValidateAll` / `ApplyDefaults` / `ConfigField.Validate`。
- `internal/modulemgr` へ移す: `ModuleManager` IF、`moduleManager` 実装、
  `NewModuleManager`、`safeName` / `safeInterval` / `safeInit` / `safeRun` /
  `runModuleLoop` / `Register` / `Unregister` / `Get` / `Start` / `Stop` /
  `NotifyConfigChanged`。
- `internal/system` へ移す: `SystemModule` / `SystemConfig` / `smartCache` /
  `processCache` / `cpuCollector` ほか。

## 5. 手順

1. `internal/system` を作り、`pkg/module/system.go`＋テストを移動。
2. `internal/modulemgr` を作り、`base.go` の manager 部分＋テストを移動。
3. `pkg/module` を契約のみに。
4. `cmd/agent` の import を更新。
5. 契約テスト（`contract_test.go`）は `pkg/module` に残す。
6. combined go.work で全プラグインを 1 回だけ再ビルド。
7. E2E/スモークで検証。

## 6. 非目標

- プラグインの import 名は変えない（`pkg/module` のまま。プラグイン作者への
  影響ゼロ）。
- `pkg/fsutil` は security プラグインの正規依存として残す（小さく保つ）。
- 契約に含めない型を増やさない（境界＝プラグインが import する型のみ）。
