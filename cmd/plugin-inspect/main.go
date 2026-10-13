// cmd/plugin-inspect/main.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"plugin"
	"reflect"
	"runtime/debug"
	"time"
)

// InspectResult は plugin-inspect が吐き出す JSON のルートや。
type InspectResult struct {
	Success      bool            `json:"success"`
	Name         string          `json:"name"`
	DisplayName  string          `json:"display_name,omitempty"`
	Description  string          `json:"description"`
	IntervalSec  float64         `json:"interval_sec"`
	Fields       json.RawMessage `json:"fields"`
	IsBackup     bool            `json:"is_backup"`
	ErrorMessage string          `json:"error_message,omitempty"`
	PanicStack   string          `json:"panic_stack,omitempty"`
	LoadedAt     string          `json:"loaded_at"`
}

func main() {
	var (
		soPath  = flag.String("so", "", "対象の .so ファイルパス")
		pretty  = flag.Bool("pretty", true, "JSON を整形して出力")
		timeout = flag.Duration("timeout", 5*time.Second, "タイムアウト")
	)
	flag.Parse()

	if *soPath == "" {
		writeError("--so が指定されていません", *pretty, "")
		os.Exit(1)
	}

	done := make(chan struct{})
	var result InspectResult

	go func() {
		defer close(done)
		result = inspect(*soPath)
	}()

	select {
	case <-done:
	case <-time.After(*timeout):
		writeError("inspect がタイムアウトしました", *pretty, "")
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "JSON 出力失敗: %v\n", err)
		os.Exit(1)
	}

	if !result.Success {
		os.Exit(1)
	}
}

// inspect はリフレクションベースで .so の情報を抽出する。
func inspect(soPath string) (res InspectResult) {
	res = InspectResult{
		LoadedAt: time.Now().Format(time.RFC3339),
	}

	defer func() {
		if r := recover(); r != nil {
			res.Success = false
			res.ErrorMessage = fmt.Sprintf("パニックを検出: %v", r)
			res.PanicStack = string(debug.Stack())
		}
	}()

	p, err := plugin.Open(soPath)
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("plugin.Open 失敗: %v", err)
		return res
	}

	sym, err := p.Lookup("Plugin")
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("シンボル Plugin が見つかりません: %v", err)
		return res
	}

	// Lookup は「変数のアドレス」を返すので、Elem() でポインタを剥がす。
	v := reflect.ValueOf(sym)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// --- GetConfigFields() をリフレクションで呼ぶ ---
	method := v.MethodByName("GetConfigFields")
	if !method.IsValid() {
		res.ErrorMessage = "Plugin は GetConfigFields メソッドを実装していません"
		return res
	}

	results := method.Call(nil)
	if len(results) == 0 {
		res.ErrorMessage = "GetConfigFields が戻り値を返しませんでした"
		return res
	}

	fieldsJSON, err := json.Marshal(results[0].Interface())
	if err != nil {
		res.ErrorMessage = fmt.Sprintf("GetConfigFields の戻り値の JSON 化失敗: %v", err)
		return res
	}
	res.Fields = json.RawMessage(fieldsJSON)
	res.Success = true

	// --- 追加情報をリフレクションで取得 ---
	if s := callString(v, "Name"); s != "" {
		res.Name = s
	}
	if s := callString(v, "DisplayName"); s != "" {
		res.DisplayName = s
	}
	if s := callString(v, "Description"); s != "" {
		res.Description = s
	}
	if d := callDurationSec(v, "Interval"); d > 0 {
		res.IntervalSec = d
	}
	if v.MethodByName("RunBackup").IsValid() {
		res.IsBackup = true
	}

	if res.Name == "" {
		res.Name = deriveNameFromPath(soPath)
	}

	return res
}

// callString は引数なし・戻り値 string のメソッドを呼ぶ。
func callString(v reflect.Value, methodName string) string {
	m := v.MethodByName(methodName)
	if !m.IsValid() {
		return ""
	}
	results := m.Call(nil)
	if len(results) == 0 {
		return ""
	}
	s, _ := results[0].Interface().(string)
	return s
}

// callDurationSec は引数なし・戻り値 time.Duration のメソッドを秒に変換して返す。
func callDurationSec(v reflect.Value, methodName string) float64 {
	m := v.MethodByName(methodName)
	if !m.IsValid() {
		return 0
	}
	results := m.Call(nil)
	if len(results) == 0 {
		return 0
	}
	raw := results[0].Interface()
	if d, ok := raw.(time.Duration); ok {
		return d.Seconds()
	}
	if i, ok := raw.(int64); ok {
		return time.Duration(i).Seconds()
	}
	return 0
}

func deriveNameFromPath(soPath string) string {
	base := soPath
	for j := len(base) - 1; j >= 0; j-- {
		if base[j] == '/' {
			base = base[j+1:]
			break
		}
	}
	if len(base) > 3 && base[len(base)-3:] == ".so" {
		base = base[:len(base)-3]
	}
	return base
}

func writeError(msg string, pretty bool, stack string) {
	res := InspectResult{
		Success:      false,
		ErrorMessage: msg,
		PanicStack:   stack,
		LoadedAt:     time.Now().Format(time.RFC3339),
	}
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(res)
}
