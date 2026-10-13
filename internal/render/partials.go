package render

import (
	"html/template"
	"io"
	"strings"
	"sync"
)

// Partials はテンプレートパーシャルを管理する構造体
type Partials struct {
	mu        sync.RWMutex
	templates *template.Template
	funcMap   template.FuncMap
}

// NewPartials は新しい Partials を作成する
func NewPartials() *Partials {
	p := &Partials{
		funcMap: defaultFuncMap(),
	}
	p.loadPartials()
	return p
}

// loadPartials はパーシャルテンプレートファイルを読み込む（go:embed を使わない）
func (p *Partials) loadPartials() {
	p.mu.Lock()
	defer p.mu.Unlock()

	tmpl := template.New("partials")
	tmpl.Funcs(p.funcMap)
	tmpl = template.Must(tmpl.ParseGlob("internal/render/templates/partials/*.html"))
	p.templates = tmpl
}

// Render は指定されたパーシャルをレンダリングする
func (p *Partials) Render(w io.Writer, name string, data interface{}) error {
	p.mu.RLock()
	tmpl := p.templates
	p.mu.RUnlock()

	if tmpl == nil {
		p.loadPartials()
		p.mu.RLock()
		tmpl = p.templates
		p.mu.RUnlock()
	}

	return tmpl.ExecuteTemplate(w, name, data)
}

// RenderString は指定されたパーシャルを文字列としてレンダリングする
func (p *Partials) RenderString(name string, data interface{}) (string, error) {
	var sb strings.Builder
	if err := p.Render(&sb, name, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// Reload はパーシャルテンプレートを再読み込みする（開発用）
func (p *Partials) Reload() {
	p.loadPartials()
}

// Merge はパーシャルテンプレートを既存のテンプレートにマージする
func (p *Partials) Merge(base *template.Template) (*template.Template, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.templates == nil {
		return base, nil
	}

	for _, tmpl := range p.templates.Templates() {
		if tmpl.Name() == "partials" {
			continue
		}
		base = template.Must(base.AddParseTree(tmpl.Name(), tmpl.Tree))
	}
	return base, nil
}
