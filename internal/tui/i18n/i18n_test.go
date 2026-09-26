package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/router"
)

// translatedLiterals 는 TUI 소스에서 T/t 의 첫 인자로 쓰인 문자열 리터럴을 모은다.
func translatedLiterals(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "T" && sel.Sel.Name != "t" && sel.Sel.Name != "toast") {
				return true
			}
			arg := call.Args[0]
			if sel.Sel.Name == "toast" && len(call.Args) > 1 {
				arg = call.Args[1]
			}
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					seen[s] = true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// needsTranslation 은 글자가 없는 형식 문자열("%v", "%s → %s")을 제외한다.
func needsTranslation(s string) bool {
	return regexp.MustCompile(`[A-Za-z]{2,}`).MatchString(regexp.MustCompile(`%[a-z]`).ReplaceAllString(s, ""))
}

func TestEveryUIStringHasKorean(t *testing.T) {
	lits := translatedLiterals(t)
	if len(lits) < 50 {
		t.Fatalf("found only %d literals; the scanner is probably broken", len(lits))
	}
	var missing []string
	for _, s := range lits {
		if _, ok := ko[s]; !ok && needsTranslation(s) {
			missing = append(missing, strconv.Quote(s))
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d strings lack a Korean translation:\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

var verb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

func TestTranslationsKeepFormatVerbs(t *testing.T) {
	for k, v := range ko {
		if a, b := verb.FindAllString(k, -1), verb.FindAllString(v, -1); strings.Join(a, ",") != strings.Join(b, ",") {
			t.Errorf("%q: verbs %v, translation has %v", k, a, b)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "ko_KR.UTF-8")
	if Detect("", "") != KO {
		t.Error("LANG=ko_KR should pick Korean")
	}
	if Detect("", "en") != EN || Detect("ko", "en") != KO {
		t.Error("override and saved setting must win over the environment")
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")
	if Detect("", "") != EN {
		t.Error("LC_ALL must win over LANG")
	}
	if KO.T("Routes") == "Routes" || EN.T("Routes") != "Routes" {
		t.Error("translation lookup broken")
	}
	if got := EN.T("no such key %d", 3); got != "no such key 3" {
		t.Errorf("fallback = %q", got)
	}
}

// 코드가 변수로 넘기는 키(배지, 카테고리, hint 단어)는 스캐너가 못 보므로 직접 확인한다.
func TestIndirectKeysHaveKorean(t *testing.T) {
	keys := []string{"CONNECTED", "DEGRADED", "DISCONNECTED", "RECONNECTING", "back", "continue with defaults"}
	for _, p := range router.Presets() {
		keys = append(keys, p.Category)
	}
	keys = append(keys, core.New(core.Options{ListenOverride: "0.0.0.0:1"}).Warnings()...)
	keys = append(keys, core.MigratedNotice)
	for _, k := range keys {
		if _, ok := ko[k]; !ok {
			t.Errorf("no Korean for %q", k)
		}
	}
}
