package match

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "sub/main.go", true},
		{"**/*.go", "a/b/c/main.go", true},
		{"**/*.go", "main.go", true},
		{"**/tests/**", "a/tests/b.py", true},
		{"**/tests/**", "tests/b.py", true},
		{"**/tests/**", "a/test/b.py", false},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/workflows/a/b.yml", true},
		{".github/workflows/**", ".github/dependabot.yml", false},
		{"orion.json", "orion.json", true},
		{"orion.json", "sub/orion.json", false},
		{"**/test_*.py", "src/test_thing.py", true},
		{"**/test_*.py", "src/thing_test.py", false},
		{"**/__tests__/**", "src/__tests__/a.ts", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"**", "anything/at/all", true},
		{"./*.go", "main.go", true},
	}
	for _, tc := range tests {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	pats := []string{"**/*_test.go", "**/tests/**"}
	if !MatchAny(pats, "internal/hook/gate_test.go") {
		t.Error("should match the test-file pattern")
	}
	if MatchAny(pats, "internal/hook/gate.go") {
		t.Error("source file must not match test patterns")
	}
	if MatchAny(nil, "anything") {
		t.Error("an empty pattern list must match nothing")
	}
}

// Windows paths arrive with backslashes; the config patterns are always
// slash-separated. Without normalization every protected-path rule would
// silently stop working on Windows.
func TestMatchNormalizesSeparators(t *testing.T) {
	if !Match("**/tests/**", `a\tests\b.py`) {
		t.Error("backslash paths must normalize before matching")
	}
}
