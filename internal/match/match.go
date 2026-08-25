// Package match implements the glob subset Orion needs, including the
// "**" segment that path.Match does not support. Kept dependency-free so
// the binary builds offline with an empty go.sum.
package match

import (
	"path"
	"path/filepath"
	"strings"
)

// Match reports whether name matches pattern. Semantics:
//
//	*   matches any sequence of non-separator characters
//	?   matches any single non-separator character
//	**  as a whole path segment matches zero or more segments
//
// Comparison is on slash-separated paths; Windows backslashes are
// normalized first. Matching is case-sensitive, matching git's behaviour.
func Match(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	name = strings.TrimPrefix(name, "./")
	pattern = strings.TrimPrefix(pattern, "./")
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, nam []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Trailing ** matches everything that remains, including nothing.
			if len(pat) == 1 {
				return true
			}
			// Try consuming 0..len(nam) segments.
			for i := 0; i <= len(nam); i++ {
				if matchSegments(pat[1:], nam[i:]) {
					return true
				}
			}
			return false
		}
		if len(nam) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], nam[0])
		if err != nil || !ok {
			return false
		}
		pat, nam = pat[1:], nam[1:]
	}
	return len(nam) == 0
}

// MatchAny reports whether name matches any pattern in the list.
func MatchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if Match(p, name) {
			return true
		}
	}
	return false
}
