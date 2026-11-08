package ui

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	enKeyRE    = regexp.MustCompile(`"((?:[^"\\]|\\.)*)":`)
	tCallRE    = regexp.MustCompile(`\bt\(\s*"((?:[^"\\]|\\.)*)"`)
	tTernRE    = regexp.MustCompile(`\bt\(\s*[^()"]*?\?\s*"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	dataAttrRE = regexp.MustCompile(`data-(?:t|title|label)="([^"]*)"`)
)

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// Every string the page shows in Chinese has its English in i18n.js: the
// t("…") calls (and t(x ? "…" : "…")) and the data-t, data-title and
// data-label attributes the page translates.
func TestEveryStringHasEnglish(t *testing.T) {
	a := Assets()
	src, err := fs.ReadFile(a, "i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	en := map[string]bool{}
	for _, m := range enKeyRE.FindAllStringSubmatch(s[strings.Index(s, "const EN"):strings.Index(s, "const ZH")], -1) {
		var k string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &k) == nil {
			en[k] = true
		}
	}
	files, _ := fs.Glob(a, "*.js")
	html, _ := fs.Glob(a, "*.html")
	for _, f := range append(files, html...) {
		if f == "i18n.js" {
			continue
		}
		b, _ := fs.ReadFile(a, f)
		var keys []string
		for _, m := range tCallRE.FindAllStringSubmatch(string(b), -1) {
			keys = append(keys, m[1])
		}
		for _, m := range tTernRE.FindAllStringSubmatch(string(b), -1) {
			keys = append(keys, m[1], m[2])
		}
		for _, m := range dataAttrRE.FindAllStringSubmatch(string(b), -1) {
			keys = append(keys, m[1])
		}
		for _, raw := range keys {
			var k string
			if json.Unmarshal([]byte(`"`+raw+`"`), &k) != nil {
				k = raw
			}
			if hasHan(k) && !en[k] {
				t.Errorf("%s: no English for %q", f, k)
			}
		}
	}
}
