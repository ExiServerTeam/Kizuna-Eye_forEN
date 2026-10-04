//go:build !linux

package main

import "errors"

// errSUIDWatchUnsupported is returned when the event-driven watch cannot be
// used on this platform. The caller then keeps the periodic scan as the only
// detection path, which is exactly the behaviour of older versions.
var errSUIDWatchUnsupported = errors.New("inotify は linux でのみ利用できます")

func (w *suidWatcher) start() error { return errSUIDWatchUnsupported }

func (w *suidWatcher) stop() {}
