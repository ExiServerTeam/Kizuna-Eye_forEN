//go:build !linux

package main

import "os"

// inodeOf is the non-linux fallback of the directory watch metadata: those
// platforms have no syscall.Stat_t here, so the comparison degrades to
// size+mtime (the detection itself stays intact).
func inodeOf(os.FileInfo) uint64 { return 0 }
