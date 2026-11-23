package pdbr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestErrorVocabularyDocumented keeps the problem code vocabulary closed:
// every string constant named Code* under internal/ is a row of a
// docs/errors.md "code vocabulary" table, and every row is such a constant.
func TestErrorVocabularyDocumented(t *testing.T) {
	inCode := map[string]string{}
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						return err
					}
					inCode[v] = path + ": " + name.Name
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("docs/errors.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	start := strings.Index(text, "## `code` vocabulary")
	end := strings.Index(text, "## Status mapping")
	if start < 0 || end < start {
		t.Fatal("docs/errors.md: code vocabulary section not found")
	}
	inDoc := map[string]bool{}
	row := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")
	for _, m := range row.FindAllStringSubmatch(text[start:end], -1) {
		if m[1] != "code" {
			inDoc[m[1]] = true
		}
	}
	var missing, stale []string
	for c, where := range inCode {
		if !inDoc[c] {
			missing = append(missing, c+" ("+where+")")
		}
	}
	for c := range inDoc {
		if _, ok := inCode[c]; !ok {
			stale = append(stale, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("codes missing from docs/errors.md: %s", strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("docs/errors.md lists codes no constant defines: %s", strings.Join(stale, ", "))
	}
}
