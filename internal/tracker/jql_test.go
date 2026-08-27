package tracker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A project key that IS a JQL reserved word must survive interpolation.
//
// Unquoted, `project = OR` is a parse error and the command fails for that
// project alone -- which is how OR-120 was found, on a project keyed OR.
func TestReservedWordsAreQuoted(t *testing.T) {
	for _, key := range []string{"OR", "AND", "ORDER", "NOT", "EMPTY", "NULL", "IN", "WAS"} {
		t.Run(key, func(t *testing.T) {
			if got, want := JQLEq("project", key), `project = "`+key+`"`; got != want {
				t.Errorf("JQLEq: got %s, want %s", got, want)
			}
			if got, want := JQLIn("project", key), `project IN ("`+key+`")`; got != want {
				t.Errorf("JQLIn: got %s, want %s", got, want)
			}
			if got, want := JQLNotIn("labels", key), `labels NOT IN ("`+key+`")`; got != want {
				t.Errorf("JQLNotIn: got %s, want %s", got, want)
			}
		})
	}
}

// A quote inside a value must not terminate the JQL string early, or the
// rest of the value is parsed as query syntax.
func TestQuotesInsideAValueAreEscaped(t *testing.T) {
	if got, want := JQLEq("labels", `a" OR key`), `labels = "a\" OR key"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestJQLSetAndJoin(t *testing.T) {
	if got, want := JQLIn("project", "OR", "AND"), `project IN ("OR", "AND")`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	// An omitted optional clause must not leave a dangling AND.
	if got, want := JQLAnd("", JQLEq("labels", "x"), ""), `labels = "x"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := JQLAnd(JQLEq("project", "OR"), JQLEq("labels", "x")),
		`project = "OR" AND labels = "x"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// The rule OR-120 actually needs: nobody hand-writes a JQL clause.
//
// The bug was one call site interpolating a project key bare while another
// quoted it, so a fix that only corrects today's four call sites regresses
// the moment a fifth is written. This walks every Go source file in the
// repository and fails on any string literal that looks like a JQL clause,
// which leaves the helpers in jql.go as the only way to build one.
func TestNoHandWrittenJQLClauses(t *testing.T) {
	// A field name followed by a comparison operator: what a JQL clause
	// looks like and what ordinary prose does not.
	clause := regexp.MustCompile(`(?i)\b(project|parent|labels|issuekey|key|status|assignee|reporter|sprint)\s*(=|!=|\bIN\b|\bNOT\s+IN\b)`)

	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		if base == "jql.go" {
			return nil // the one place clauses are built
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if clause.MatchString(s) {
				t.Errorf("%s: hand-written JQL clause %q\n"+
					"  build it with tracker.JQLEq/JQLIn/JQLNotIn/JQLAnd instead:\n"+
					"  a bare value breaks on a reserved word such as a project keyed OR",
					fset.Position(lit.Pos()), s)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
