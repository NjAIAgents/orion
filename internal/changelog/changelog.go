// Package changelog collates per-ticket changelog fragments into CHANGELOG.md.
//
// Every ticket used to append its entry to the same `## Unreleased` section of
// the same file, so any two branches in flight conflicted there regardless of
// what code they touched -- three tickets partitioned the code cleanly across
// three packages and still blocked each other on CHANGELOG.md alone. With
// strict branch protection the cost compounds: each merge invalidates every
// other open pull request, and every one of those rebases stops on that file.
//
// So an implementer writes `.changelog.d/<KEY>.md` instead. A new file per
// ticket means two tickets never touch the same path and the conflict cannot
// occur -- prevention rather than resolution, which matters because there is
// direct evidence that resolving these by hand goes wrong: one hand-merged
// conflict kept both sides (correct-looking, both were additive) and shipped
// two `### Changed` sections carrying the same bullet to main. The merge
// actually needed was "keep both, then merge same-named sections, then
// deduplicate" -- three rules, applied consistently, on a file nobody reads
// carefully during a rebase.
//
// Nothing changes for a reader of CHANGELOG.md. Collation emits the same file
// the old process produced, with the section order fixed rather than left to
// whoever wrote the entry.
package changelog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is the fragment directory, relative to the repository root.
const Dir = ".changelog.d"

// Sections is the keepachangelog order, and it is the whole order: a fragment
// naming anything else is an error rather than a section invented on the spot.
// Today's CHANGELOG.md is already inconsistent -- Unreleased had Fixed before
// Changed while v0.5.1 has Changed before Fixed -- and inconsistent order makes
// every merge harder than it needs to be. Collating makes it automatic instead
// of a thing every agent must remember.
var Sections = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

// Fragment is one ticket's entry: its key and its body, grouped by section.
type Fragment struct {
	Key  string // the ticket key, from the filename
	Path string // path as given to Load, for error messages
	Body map[string][]string
}

// Path returns where the fragment for a ticket key belongs.
func Path(root, key string) string {
	return filepath.Join(root, Dir, strings.ToUpper(strings.TrimSpace(key))+".md")
}

// canonical resolves a heading to its section name, case-insensitively.
func canonical(name string) (string, bool) {
	for _, s := range Sections {
		if strings.EqualFold(s, name) {
			return s, true
		}
	}
	return "", false
}

// Parse reads one fragment.
//
// The shape is a keepachangelog section heading and the entry beneath it:
//
//	### Added
//	- What a reader needs to know.
//
// An unknown section is refused rather than dropped or guessed at. A fragment
// that silently vanished at collation would be worse than one that never
// existed: the ticket did the work of writing an entry and the release would
// simply not have it, with nothing saying so.
func Parse(path string, content string) (map[string][]string, error) {
	body := map[string][]string{}
	section := ""
	var buf []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		if section != "" && text != "" {
			body[section] = append(body[section], text)
		}
	}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			name := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			sec, ok := canonical(name)
			if !ok {
				return nil, fmt.Errorf(
					"%s names the unknown section %q; valid sections are %s",
					path, name, strings.Join(Sections, ", "))
			}
			flush()
			section = sec
			continue
		}
		if section == "" && trimmed != "" {
			return nil, fmt.Errorf(
				"%s has content before any section heading; start it with one of: %s",
				path, strings.Join(Sections, ", "))
		}
		buf = append(buf, line)
	}
	flush()

	if len(body) == 0 {
		return nil, fmt.Errorf("%s is empty; delete it or give it a section and an entry", path)
	}
	return body, nil
}

// Load reads every fragment in root's fragment directory, sorted by key.
//
// Only `.md` files are read. The directory carries a `.gitkeep` so it survives
// in git while empty, and that file is not an entry.
func Load(root string) ([]Fragment, error) {
	dir := filepath.Join(root, Dir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var out []Fragment
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		rel := filepath.ToSlash(filepath.Join(Dir, e.Name()))
		body, err := Parse(rel, string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, Fragment{
			Key:  strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			Path: p,
			Body: body,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Render turns fragments into the version section, in keepachangelog order.
func Render(version string, frags []Fragment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", version)
	for _, sec := range Sections {
		var blocks []string
		for _, f := range frags {
			blocks = append(blocks, f.Body[sec]...)
		}
		if len(blocks) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", sec)
		b.WriteString(strings.Join(blocks, "\n\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// Insert places a rendered section into an existing changelog.
//
// It goes after `## Unreleased` and before the previous release, which is
// where a reader looks for it. A changelog with no releases yet gets the
// section appended.
func Insert(doc, section string) string {
	lines := strings.Split(doc, "\n")
	at := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), "unreleased") {
			continue
		}
		at = i
		break
	}
	if at < 0 {
		out := strings.TrimRight(doc, "\n")
		return out + "\n\n" + section
	}
	head := strings.Join(lines[:at], "\n")
	tail := strings.Join(lines[at:], "\n")
	return strings.TrimRight(head, "\n") + "\n\n" + section + "\n" + tail
}

// Result records what a collation did, so the caller reports rather than
// claims.
type Result struct {
	Version string
	Keys    []string // fragment keys collated, in the order rendered
	Removed []string // fragment paths deleted
}

// Collate writes the fragments into CHANGELOG.md and deletes them.
//
// Deleting is part of the same change on purpose: a fragment that survives
// collation is an entry that appears in the next release as well.
func Collate(root, version string) (*Result, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("a version is required to collate fragments (--version vX.Y.Z)")
	}

	frags, err := Load(root)
	if err != nil {
		return nil, err
	}
	if len(frags) == 0 {
		return nil, fmt.Errorf("no fragments in %s/: nothing to collate", Dir)
	}

	p := filepath.Join(root, "CHANGELOG.md")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	doc := string(b)

	// A second collation of the same version would append a duplicate section
	// rather than notice, and the fragments would already be gone.
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") &&
			strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), version) {
			return nil, fmt.Errorf("CHANGELOG.md already has a %s section; "+
				"collate under a different version, or remove that section first", version)
		}
	}

	res := &Result{Version: version}
	for _, f := range frags {
		res.Keys = append(res.Keys, f.Key)
	}

	if err := os.WriteFile(p, []byte(Insert(doc, Render(version, frags))), 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", p, err)
	}
	for _, f := range frags {
		if err := os.Remove(f.Path); err != nil {
			// The changelog is already written, so this is not fatal -- but a
			// surviving fragment repeats itself in the next release, which is
			// exactly the silent duplicate this whole mechanism exists to
			// avoid. Say it loudly.
			return res, fmt.Errorf("CHANGELOG.md was written, but %s could not be "+
				"deleted (%w); delete it before committing or it ships twice", f.Path, err)
		}
		res.Removed = append(res.Removed, f.Path)
	}
	return res, nil
}
