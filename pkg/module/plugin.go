package module

// PluginModule は Go プラグインとして実装されたモジュールだ。
// プラグインは NewPluginModule という名前のシンボルをエクスポートする必要がある。
// PluginModule is a module implemented as a Go plugin.
// The plugin must export a symbol named NewPluginModule.
type PluginModule interface {
	Module
}

// PluginConstructor はプラグインのコンストラクタ型だ。
// プラグインはこのシグネチャの関数を NewPluginModule としてエクスポートする。
// PluginConstructor is the plugin constructor type.
// The plugin exports a function of this signature as NewPluginModule.
type PluginConstructor func(logger Logger) PluginModule
