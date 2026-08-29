package orion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoHandRolledOnActivity pins the property OR-176 names as the one that
// would have caught it: no supervised run may wire OnActivity with an inline
// closure. work.ActivityLogger (and cmd/orion's fixActivity, which wraps it)
// is the only thing that may sit on the right-hand side of an OnActivity
// assignment -- a second, hand-rolled implementation is how the fix loop
// ended up printing unattributed console lines and emitting nothing to the
// event log at all.
func TestNoHandRolledOnActivity(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	fset := token.NewFileSet()

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			var value ast.Expr
			switch node := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "OnActivity" && i < len(node.Rhs) {
						value = node.Rhs[i]
					}
				}
			case *ast.KeyValueExpr:
				ident, ok := node.Key.(*ast.Ident)
				if ok && ident.Name == "OnActivity" {
					value = node.Value
				}
			}
			if value == nil {
				return true
			}
			if _, isFuncLit := value.(*ast.FuncLit); isFuncLit {
				offenders = append(offenders,
					path+": OnActivity wired to an inline closure instead of work.ActivityLogger")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range offenders {
		t.Error(o)
	}
}
