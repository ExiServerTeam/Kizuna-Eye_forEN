package render

import (
	"fmt"
	"html/template"
	"io"
	"strings"
	"sync"
	"time"
)

// Layout はテンプレートレイアウトを管理する構造体
type Layout struct {
	mu        sync.RWMutex
	templates *template.Template
	funcMap   template.FuncMap
}

// NewLayout は新しいレイアウトを作成する
func NewLayout() *Layout {
	l := &Layout{
		funcMap: defaultFuncMap(),
	}
	l.loadTemplates()
	return l
}

// defaultFuncMap はテンプレートで使用できるカスタム関数を返す
func defaultFuncMap() template.FuncMap {
	return template.FuncMap{
		"now": func() time.Time { return time.Now() },
		"formatTime": func(t time.Time, layout string) string {
			if layout == "" {
				layout = "2006-01-02 15:04:05"
			}
			return t.Format(layout)
		},
		"formatUptime": func(seconds uint64) string {
			d := seconds / 86400
			h := (seconds % 86400) / 3600
			m := (seconds % 3600) / 60
			s := seconds % 60

			parts := []string{}
			if d > 0 {
				parts = append(parts, fmt.Sprintf("%d日", d))
			}
			if h > 0 {
				parts = append(parts, fmt.Sprintf("%d時間", h))
			}
			if m > 0 {
				parts = append(parts, fmt.Sprintf("%d分", m))
			}
			if s > 0 {
				parts = append(parts, fmt.Sprintf("%d秒", s))
			}
			if len(parts) == 0 {
				return "0秒"
			}
			return strings.Join(parts, " ")
		},
		"percent": func(v float64) string {
			return fmt.Sprintf("%.1f%%", v)
		},
		"bytes": func(b uint64) string {
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := uint64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			switch exp {
			case 0:
				return fmt.Sprintf("%.1f KB", float64(b)/float64(div))
			case 1:
				return fmt.Sprintf("%.1f MB", float64(b)/float64(div))
			case 2:
				return fmt.Sprintf("%.1f GB", float64(b)/float64(div))
			default:
				return fmt.Sprintf("%.1f TB", float64(b)/float64(div))
			}
		},
	}
}

// loadTemplates はテンプレートファイルを読み込む（go:embed を使わない）
func (l *Layout) loadTemplates() {
	l.mu.Lock()
	defer l.mu.Unlock()

	tmpl := template.New("")
	tmpl.Funcs(l.funcMap)
	tmpl = template.Must(tmpl.ParseGlob("internal/render/templates/*.html"))
	l.templates = tmpl
}

// Render は指定されたテンプレートをレンダリングする
func (l *Layout) Render(w io.Writer, name string, data interface{}) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.templates == nil {
		l.mu.RUnlock()
		l.loadTemplates()
		l.mu.RLock()
		defer l.mu.RUnlock()
	}

	return l.templates.ExecuteTemplate(w, name, data)
}

// Reload はテンプレートを再読み込みする（開発用）
func (l *Layout) Reload() {
	l.loadTemplates()
}
