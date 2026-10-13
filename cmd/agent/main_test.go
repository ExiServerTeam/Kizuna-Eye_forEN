package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/pkg/module"
)

// fakeBackupModule implements BackupRunner + BackupStatusProvider.
type fakeBackupModule struct {
	runErr error
	size   int64
}

func (m *fakeBackupModule) Name() string                        { return "fake_backup" }
func (m *fakeBackupModule) Description() string                 { return "fake" }
func (m *fakeBackupModule) Interval() time.Duration             { return time.Hour }
func (m *fakeBackupModule) Init(ctx context.Context) error      { return nil }
func (m *fakeBackupModule) Run(ctx context.Context) error       { return nil }
func (m *fakeBackupModule) RunBackup(ctx context.Context) error { return m.runErr }
func (m *fakeBackupModule) GetBackupStatus() *module.BackupStatus {
	return &module.BackupStatus{Name: "fake_backup", Size: m.size, Status: "success"}
}

// fakeManager is a minimal ModuleManager for command tests.
type fakeManager struct {
	mods map[string]module.Module
}

func (m *fakeManager) Register(ctx context.Context, mod module.Module) error {
	m.mods[mod.Name()] = mod
	return nil
}
func (m *fakeManager) Unregister(name string) error          { delete(m.mods, name); return nil }
func (m *fakeManager) Get(name string) (module.Module, bool) { mod, ok := m.mods[name]; return mod, ok }
func (m *fakeManager) Start(ctx context.Context)             {}
func (m *fakeManager) Stop()                                 {}
func (m *fakeManager) NotifyConfigChanged(name string)       {}

// wsServerPair sets up a server/client websocket pair and returns the writer
// for the agent side plus a channel of messages received by the server.
func wsServerPair(t *testing.T) (*wsWriter, <-chan map[string]interface{}) {
	t.Helper()
	msgs := make(chan map[string]interface{}, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]interface{}
			if json.Unmarshal(data, &m) == nil {
				msgs <- m
			}
		}
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &wsWriter{conn: conn}, msgs
}

func TestHandleDashboardCommandUnknownPlugin(t *testing.T) {
	w, msgs := wsServerPair(t)
	mgr := &fakeManager{mods: map[string]module.Module{}}

	cmd, _ := json.Marshal(map[string]string{"action": "run_backup", "plugin": "missing", "request_id": "r1"})
	handleDashboardCommand(context.Background(), cmd, mgr, w, nil)

	select {
	case m := <-msgs:
		if m["status"] != "error" || m["request_id"] != "r1" {
			t.Fatalf("unexpected reply: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply for unknown plugin")
	}
}

func TestHandleDashboardCommandSuccess(t *testing.T) {
	w, msgs := wsServerPair(t)
	mgr := &fakeManager{mods: map[string]module.Module{}}
	mod := &fakeBackupModule{size: 4242}
	mgr.mods[mod.Name()] = mod

	cmd, _ := json.Marshal(map[string]string{"action": "run_backup", "plugin": "fake_backup", "request_id": "r2"})
	handleDashboardCommand(context.Background(), cmd, mgr, w, nil)

	select {
	case m := <-msgs:
		if m["status"] != "success" || m["request_id"] != "r2" {
			t.Fatalf("unexpected reply: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply for successful run")
	}
}

// A plugin that is not a BackupRunner must return an error, not panic.
type notBackupModule struct{}

func (m *notBackupModule) Name() string                   { return "not_backup" }
func (m *notBackupModule) Description() string            { return "x" }
func (m *notBackupModule) Interval() time.Duration        { return time.Hour }
func (m *notBackupModule) Init(ctx context.Context) error { return nil }
func (m *notBackupModule) Run(ctx context.Context) error  { return nil }

func TestHandleDashboardCommandNonRunner(t *testing.T) {
	w, msgs := wsServerPair(t)
	mgr := &fakeManager{mods: map[string]module.Module{"not_backup": &notBackupModule{}}}

	cmd, _ := json.Marshal(map[string]string{"action": "run_backup", "plugin": "not_backup", "request_id": "r3"})
	handleDashboardCommand(context.Background(), cmd, mgr, w, nil)

	select {
	case m := <-msgs:
		if m["status"] != "error" {
			t.Fatalf("non-runner should error: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
}

// Malformed command must not panic.
func TestHandleDashboardCommandBadJSON(t *testing.T) {
	w, _ := wsServerPair(t)
	mgr := &fakeManager{mods: map[string]module.Module{}}
	handleDashboardCommand(context.Background(), []byte("{not json"), mgr, w, nil)
}

// collectBackupStatus picks the newest LastRun across providers.
type statusModule struct{ st *module.BackupStatus }

func (m *statusModule) Name() string                          { return "s" }
func (m *statusModule) Description() string                   { return "s" }
func (m *statusModule) Interval() time.Duration               { return time.Hour }
func (m *statusModule) Init(ctx context.Context) error        { return nil }
func (m *statusModule) Run(ctx context.Context) error         { return nil }
func (m *statusModule) GetBackupStatus() *module.BackupStatus { return m.st }

func TestCollectBackupStatusPicksNewest(t *testing.T) {
	// Save and restore the global loadedPlugins set.
	loadedPlugins.Lock()
	old := loadedPlugins.names
	loadedPlugins.names = map[string]bool{"old": true, "new": true}
	loadedPlugins.Unlock()
	defer func() { loadedPlugins.Lock(); loadedPlugins.names = old; loadedPlugins.Unlock() }()

	mgr := &fakeManager{mods: map[string]module.Module{
		"old": &statusModule{st: &module.BackupStatus{Name: "old", LastRun: "2026-01-01T00:00:00Z", Size: 1}},
		"new": &statusModule{st: &module.BackupStatus{Name: "new", LastRun: "2026-09-01T00:00:00Z", Size: 2}},
	}}

	got := collectBackupStatus(mgr)
	if got == nil || got.Name != "new" {
		t.Fatalf("expected newest (new), got %+v", got)
	}
}

var _ = sync.Mutex{}
