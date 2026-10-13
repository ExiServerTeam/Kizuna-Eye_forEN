package api

import (
	"net/http"
	"runtime"
	"time"
)

// Version is the app version. Overridable via -ldflags at build time.
var Version = "v0.7.1"

// BuildTime is the build timestamp (optional).
var BuildTime = ""

// VersionHandler serves version information.
type VersionHandler struct {
	startedAt time.Time
}

// NewVersionHandler creates a VersionHandler.
func NewVersionHandler() *VersionHandler {
	return &VersionHandler{startedAt: time.Now()}
}

// RegisterRoutes registers the version route.
func (h *VersionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/version", h.handleVersion)
}

// handleVersion returns version and runtime info.
func (h *VersionHandler) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":        Version,
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"num_cpu":        runtime.NumCPU(),
		"started_at":     h.startedAt.UTC().Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"build_time":     BuildTime,
	})
}
