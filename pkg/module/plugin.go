package module

// PluginModule は Go プラグインとして実装されるモジュールのインターフェース
// プラグインは NewPluginModule というシンボルをエクスポートする必要がある
type PluginModule interface {
	Module
}

// PluginConstructor はプラグインのコンストラクタ関数の型
// プラグインはこのシグネチャの関数を NewPluginModule としてエクスポートする
type PluginConstructor func(logger Logger) PluginModule
