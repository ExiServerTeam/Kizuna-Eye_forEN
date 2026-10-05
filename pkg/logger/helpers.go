package logger

// The *T methods live on the concrete *Logger. Some packages (internal/api,
// internal/auth) accept a minimal Logger interface so they stay testable and
// so plugins (which only ever see module.Logger) are unaffected. These
// helpers adapt such an interface: when the value is a *Logger they use the
// JSON Lines *T entry (message + message_en); otherwise they fall back to the
// plain method with the key as the format string.
//
// pkg/logger is NOT imported by any plugin (verified), so adding this file
// does not change any plugin's build hash and does not force a plugin
// rebuild. It must not import pkg/module either, for the same reason.

type infoLogger interface {
	Info(format string, args ...interface{})
}
type warnLogger interface {
	Warn(format string, args ...interface{})
}
type errorLogger interface {
	Error(format string, args ...interface{})
}
type debugLogger interface {
	Debug(format string, args ...interface{})
}

func LogInfo(l infoLogger, key string, args ...interface{}) {
	if l == nil {
		return
	}
	if t, ok := l.(interface{ InfoT(string, ...interface{}) }); ok {
		t.InfoT(key, args...)
		return
	}
	l.Info(key, args...)
}

func LogWarn(l warnLogger, key string, args ...interface{}) {
	if l == nil {
		return
	}
	if t, ok := l.(interface{ WarnT(string, ...interface{}) }); ok {
		t.WarnT(key, args...)
		return
	}
	l.Warn(key, args...)
}

func LogError(l errorLogger, key string, args ...interface{}) {
	if l == nil {
		return
	}
	if t, ok := l.(interface{ ErrorT(string, ...interface{}) }); ok {
		t.ErrorT(key, args...)
		return
	}
	l.Error(key, args...)
}

func LogDebug(l debugLogger, key string, args ...interface{}) {
	if l == nil {
		return
	}
	if t, ok := l.(interface{ DebugT(string, ...interface{}) }); ok {
		t.DebugT(key, args...)
		return
	}
	l.Debug(key, args...)
}
