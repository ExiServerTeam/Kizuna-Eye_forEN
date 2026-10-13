package module

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// The shared surface in this package is the contract between the host and every
// .so plugin. Changing it silently is dangerous: Go plugin type identity is
// per-build, so a method/field change also forces every plugin to be rebuilt
// (same source), and an accidental change can break the whole ecosystem. These
// tests pin the documented SDK (design ch.9) so any change is a deliberate,
// reviewed edit that fails a test first.

// methodSet returns the sorted method names of the interface type t.
func methodSet(t reflect.Type) []string {
	out := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	sort.Strings(out)
	return out
}

func assertMethods(t *testing.T, name string, typ reflect.Type, want []string) {
	t.Helper()
	got := methodSet(typ)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s methods = %v, want %v (shared SDK surface changed; rebuild all plugins and update this test)", name, got, want)
	}
}

func TestModuleInterfaceContract(t *testing.T) {
	assertMethods(t, "Module", reflect.TypeOf((*Module)(nil)).Elem(),
		[]string{"Name", "Interval", "Run", "Description", "Init"})
	assertMethods(t, "Logger", reflect.TypeOf((*Logger)(nil)).Elem(),
		[]string{"Info", "Warn", "Error", "Debug"})
	assertMethods(t, "PluginModule", reflect.TypeOf((*PluginModule)(nil)).Elem(),
		[]string{"Name", "Interval", "Run", "Description", "Init"})
}

func TestOptionalProviderContracts(t *testing.T) {
	assertMethods(t, "ConfigProvider", reflect.TypeOf((*ConfigProvider)(nil)).Elem(),
		[]string{"GetConfigFields"})
	assertMethods(t, "DisplayNameProvider", reflect.TypeOf((*DisplayNameProvider)(nil)).Elem(),
		[]string{"DisplayName"})
	assertMethods(t, "TagProvider", reflect.TypeOf((*TagProvider)(nil)).Elem(),
		[]string{"Tag"})
	assertMethods(t, "DashboardCardProvider", reflect.TypeOf((*DashboardCardProvider)(nil)).Elem(),
		[]string{"DashboardCards"})
	assertMethods(t, "WebAssetProvider", reflect.TypeOf((*WebAssetProvider)(nil)).Elem(),
		[]string{"WebUIPath", "WebAssets"})
	assertMethods(t, "BackupStatusProvider", reflect.TypeOf((*BackupStatusProvider)(nil)).Elem(),
		[]string{"GetBackupStatus"})
	assertMethods(t, "BackupRunner", reflect.TypeOf((*BackupRunner)(nil)).Elem(),
		[]string{"RunBackup"})
	assertMethods(t, "SecurityEventProvider", reflect.TypeOf((*SecurityEventProvider)(nil)).Elem(),
		[]string{"DrainSecurityEvents"})
	assertMethods(t, "SecurityEventRequeuer", reflect.TypeOf((*SecurityEventRequeuer)(nil)).Elem(),
		[]string{"RequeueSecurityEvents"})
}

// jsonKeys marshals v and returns the sorted top-level JSON keys.
func jsonKeys(t *testing.T, v interface{}) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertKeys(t *testing.T, name string, v interface{}, want []string) {
	t.Helper()
	got := jsonKeys(t, v)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s JSON keys = %v, want %v (cross-boundary wire format changed; rebuild all plugins and update this test)", name, got, want)
	}
}

func TestSharedStructWireFormat(t *testing.T) {
	// Every documented field is populated so omitempty does not hide it: a
	// zero value would drop omitempty fields and the test would not actually
	// exercise the wire format.

	// SecurityEvent: the dashboard/plugin event wire shape (design ch.9).
	assertKeys(t, "SecurityEvent", SecurityEvent{
		Plugin: "p", Category: "c", Level: "warning", Title: "t", Message: "m",
	}, []string{
		"plugin", "category", "level", "title", "message", "timestamp",
	})

	// BackupStatus: the fields sendStatus copies into the dashboard payload.
	assertKeys(t, "BackupStatus", BackupStatus{
		Name: "n", LastRun: "r", Status: "s", Size: 1, NextRun: "x",
	}, []string{
		"name", "last_run", "status", "size", "next_run",
	})

	// ConfigField: the form the dashboard renders from GetConfigFields().
	assertKeys(t, "ConfigField", ConfigField{
		Key: "k", Label: "l", Type: FieldText,
	}, []string{
		"key", "label", "type",
	})

	// DashboardCard: the card a module contributes.
	assertKeys(t, "DashboardCard", DashboardCard{
		ID: "i", Title: "t", Kind: "k",
	}, []string{
		"id", "title", "kind",
	})
}
