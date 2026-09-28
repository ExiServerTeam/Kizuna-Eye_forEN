package module

// PluginModule is a module implemented as a Go plugin.
// The plugin must export a symbol named NewPluginModule.
type PluginModule interface {
	Module
}

// PluginConstructor is the plugin constructor type.
// The plugin exports a function of this signature as NewPluginModule.
type PluginConstructor func(logger Logger) PluginModule
