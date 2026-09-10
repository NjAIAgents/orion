package web

// The source-guard half of OR-276's contract, split out from
// escaping_test.go's combined banned-pattern walk into one assertion per
// case so each has its own failure message and can't regress silently inside
// a bigger loop: template.URL, template.CSS and template.Srcset each get
// their own scan, the walk itself is checked against the empty-package false
// positive, and the comment-stripping the whole guard depends on is checked
// against a fixture built to exercise it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bannedInWebSource walks the same non-test .go files
// TestNoWebSourceMakesAScannedValueRawMarkup walks, and reports every file
// whose comment-stripped source contains pattern.
func bannedInWebSource(t *testing.T, pattern string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		code, cerr := codeWithoutComments(path)
		if cerr != nil {
			return cerr
		}
		if strings.Contains(code, pattern) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web sources: %v", err)
	}
	return hits
}

// template.URL skips URL escaping outright, so a scanned value converted to
// it turns a "javascript:" summary into a live, clickable link instead of
// text -- the same failure mode as template.HTML, one type over.
func TestNoWebSourceUsesTemplateURLConversion(t *testing.T) {
	if hits := bannedInWebSource(t, "template.URL"); len(hits) > 0 {
		t.Errorf("template.URL used in %v: asserts a value is an already-safe URL, which no tracker or log value is (OR-276)", hits)
	}
}

// template.CSS asserts a value is already-safe style; converting a scanned
// value to it reopens the same hole html/template's contextual escaping
// exists to close, just inside a style attribute instead of markup.
func TestNoWebSourceUsesTemplateCSSConversion(t *testing.T) {
	if hits := bannedInWebSource(t, "template.CSS"); len(hits) > 0 {
		t.Errorf("template.CSS used in %v: asserts a value is already-safe style, which no tracker or log value is (OR-276)", hits)
	}
}

// template.Srcset skips srcset escaping specifically; a scanned value
// converted to it can redirect an img/source element's fetch.
func TestNoWebSourceUsesTemplateSrcsetConversion(t *testing.T) {
	if hits := bannedInWebSource(t, "template.Srcset"); len(hits) > 0 {
		t.Errorf("template.Srcset used in %v: asserts a value is already-safe srcset, which no tracker or log value is (OR-276)", hits)
	}
}

// A guard that walks an empty result set passes on everything, including a
// package the walk never actually reached -- a wrong "." argument or a build
// tag excluding every file would fail closed as "no findings" rather than
// erroring. This pins that the walk this guard depends on really does reach
// at least one real, non-test .go file in the web package.
func TestSourceGuardScanFindsAtLeastOneGoFile(t *testing.T) {
	scanned := 0
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		return nil
	})
	if err != nil {
		t.Fatalf("walk web sources: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no non-test Go file was scanned; the source guard would pass vacuously on an empty package")
	}
}

// The guard strips comments before matching precisely so the rule stays
// writable in the prose that explains it -- this file and escaping_test.go
// both name template.HTML, template.URL and friends in doc comments. A guard
// that matched raw source would fail on its own documentation and get
// deleted rather than obeyed; this pins that a banned pattern appearing only
// in a comment does not trip it.
func TestSourceGuardDoesNotTriggerOnBannedPatternsInsideComments(t *testing.T) {
	const fixture = `package web

// Do not use template.HTML, template.JS, template.URL, template.CSS or
// template.Srcset here -- see OR-276.
func onlyMentionsBannedTypesInComments() string {
	return "no banned identifier in the code below this line"
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for _, pattern := range []string{"template.HTML", "template.JS", "template.URL", "template.CSS", "template.Srcset"} {
		if !strings.Contains(fixture, pattern) {
			t.Fatalf("fixture is not exercising the rule: raw source does not even contain %q", pattern)
		}
	}

	code, err := codeWithoutComments(path)
	if err != nil {
		t.Fatalf("codeWithoutComments: %v", err)
	}
	for _, pattern := range []string{"template.HTML", "template.JS", "template.URL", "template.CSS", "template.Srcset"} {
		if strings.Contains(code, pattern) {
			t.Errorf("comment-stripped fixture still contains %q, so the guard would flag a comment-only mention", pattern)
		}
	}
}
