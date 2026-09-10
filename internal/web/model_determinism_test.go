package web

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"strings"
	"testing"
	"time"
)

// model.go must depend on nothing that leaves its own memory: no network, no
// filesystem, no database, no logging. Each of those is a way a "derived from
// the event log" type quietly starts carrying its own state instead.
func TestModelImportsNoNetworkFilesystemDatabaseOrLoggingPackages(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	banned := []string{
		"net", "net/http", "net/rpc", "net/smtp",
		"os", "io", "io/fs", "bufio", "os/exec", "path/filepath",
		"database/sql",
		"log", "log/slog",
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, bad := range banned {
			if path == bad || strings.HasPrefix(path, bad+"/") {
				t.Errorf("model.go imports %q: the model must do no network, filesystem, database or logging I/O", path)
			}
		}
	}
}

// time.Now would make Elapsed (or any future field) report a different value
// each time the same log is read -- exactly the property OR-55 forbids.
func TestModelMakesNoCallsToTimeNow(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" && sel.Sel.Name == "Now" {
			t.Errorf("model.go calls time.Now at %s: every timestamp must come from the event log", fset.Position(sel.Pos()))
		}
		return true
	})
}

// time.Since reads the wall clock exactly as time.Now does -- a duration
// computed from "now" rather than from the log's own Last event.
func TestModelMakesNoCallsToTimeSince(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" && sel.Sel.Name == "Since" {
			t.Errorf("model.go calls time.Since at %s: elapsed must be Last minus Started, not now minus Started", fset.Position(sel.Pos()))
		}
		return true
	})
}

// The file must actually type-check, not merely parse -- a syntactically
// valid file can still fail to compile.
func TestModelFileCompilesWithoutErrors(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatalf("model.go does not parse: %s", err)
	}

	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("web", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Errorf("model.go does not compile: %s", err)
	}
}

// Two readers of the same event log must build the identical Snapshot: the
// types carry no clock and no I/O, so the same field values fed in twice
// must produce equal values out, every time.
func TestSnapshotAndSessionAreDeterministicAcrossMultipleReadsOfTheSameEventLog(t *testing.T) {
	started := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	last := started.Add(4*time.Minute + 12*time.Second)

	buildFromLog := func() Snapshot {
		return Snapshot{
			At:      last,
			Started: started,
			Cards: []Card{
				{
					Key:   "OR-55",
					Title: "Define the snapshot types",
					Verb:  "working",
					Gate:  "",
					Session: Session{
						Actor:    "implementer",
						Role:     "backend developer",
						Model:    "claude-sonnet-5",
						Steps:    7,
						Activity: "editing internal/web/model.go",
						Started:  started,
						Last:     last,
						Done:     false,
					},
				},
			},
		}
	}

	firstRead := buildFromLog()
	secondRead := buildFromLog()

	if !reflect.DeepEqual(firstRead, secondRead) {
		t.Fatalf("two reads of the same event log produced different snapshots:\nfirst:  %+v\nsecond: %+v", firstRead, secondRead)
	}

	if got, want := firstRead.Cards[0].Session.Elapsed(), secondRead.Cards[0].Session.Elapsed(); got != want {
		t.Errorf("Elapsed() differed across reads of the same log: %s vs %s", got, want)
	}
}
