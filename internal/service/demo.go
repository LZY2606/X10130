package service

import (
	"encoding/json"
	"fmt"
)

// DemoData is the bundled demonstration catalog set.
type DemoData struct {
	Base    map[string]string            `json:"base"`
	Targets map[string]map[string]string `json:"targets"`
}

// SeedDemo imports a built-in catalog set exhibiting each issue class.
func (s *Service) SeedDemo(opID string) (*DemoData, error) {
	base := map[string]string{
		"app.title":    "消息目录校验",
		"greeting":     "你好 {name}，你有 {count, plural, one {# 条消息} other {# 条消息}}。",
		"welcome.rich": "欢迎，<b>{name}</b>！",
		"items.count":  "{count, plural, one {1 项} other {# 项}}",
	}
	ja := map[string]string{
		"app.title": "メッセージカタログ検証",
		// greeting missing entirely -> missing_key
		"welcome.rich": "ようこそ、<b>{name}</b>！",
		"items.count":  "{count, plural, other {# 件}}", // missing "one"
		"ja.extra":     "追加キー",                         // extra_key
	}
	fr := map[string]string{
		"app.title":    "Validation du catalogue",
		"greeting":     "Bonjour {name}, vous avez {count, number} messages.", // type drift + missing plural
		"welcome.rich": "Bienvenue, <i>{name}</i> !",                          // tag mismatch
		"items.count":  "{count, plural, one {1 article} other {# articles}}",
		"fr.only":      "supplémentaire", // extra
	}
	d := &DemoData{Base: base, Targets: map[string]map[string]string{"ja": ja, "fr": fr}}
	for _, m := range []struct {
		lang, role string
		msgs       map[string]string
	}{
		{"zh", roleBaseline, base},
		{"ja", roleTarget, ja},
		{"fr", roleTarget, fr},
	} {
		b, err := json.Marshal(m.msgs)
		if err != nil {
			return nil, err
		}
		if _, _, err := s.Import(opID+"-"+m.lang, ImportRequest{
			Language: m.lang, Role: m.role, Filename: m.lang + ".json", Content: b,
		}); err != nil {
			return nil, fmt.Errorf("seed import %s: %w", m.lang, err)
		}
	}
	return d, nil
}
