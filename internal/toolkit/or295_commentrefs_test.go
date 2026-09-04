package toolkit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// OR-295 promised a mechanical rename: every njagents.X reference becomes
// toolkit.X, including in comments and doc strings, not just in code the
// compiler checks. or295_toolkit_rename_test.go's
// TestNjagentsSelectorHasZeroHits sweeps *code* (AST selector expressions),
// which by construction cannot see a stale reference sitting in a comment,
// e.g. "call njagents.Discover to find the clone". This sweeps comments
// specifically, across the whole module, for exactly that.
var njagentsDotWord = regexp.MustCompile(`\bnjagents\.[A-Za-z]`)

func TestNoCommentMentionsNjagentsDotSelector(t *testing.T) {
	root := repoRootForOR295(t)

	var offenders []string
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// THIS TICKET'S OWN TESTS ARE EXEMPT. They describe the rename, so
		// they say "njagents.X" on purpose -- "every njagents.X reference
		// becomes toolkit.X" is the sentence stating the rule, and a sweep
		// that fails on its own explanatory prose reports the rename as
		// incomplete when it is done.
		if strings.HasPrefix(filepath.Base(path), "or295_") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
		if perr != nil {
			// A sibling writer's half-finished file; not this test's concern.
			return nil
		}
		for _, group := range file.Comments {
			for _, c := range group.List {
				if njagentsDotWord.MatchString(c.Text) {
					rel, _ := filepath.Rel(root, path)
					offenders = append(offenders, rel+": "+strings.TrimSpace(c.Text))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range offenders {
		t.Errorf("stale comment reference to njagents.<Selector>: %s", o)
	}
}
