package web

// THE CASE THIS EXISTS TO FIX (OR-434, and again in OR-438's stage-flow
// panel): htm's tagged-template parser (ui/vendor/htm.module.js) is a
// hand-rolled tag/attribute tokenizer over the raw template string -- it
// never runs an HTML entity-decode pass. A named entity (&middot;) or a
// numeric one (&#10003;) written as template text comes out on screen as
// those literal characters, not the glyph they name, and nothing short of
// looking at a live screenshot caught it either time.
//
// So every served panel is scanned here instead: the real Unicode character
// belongs directly in the JS source, never an entity of any form.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// entityRe matches both forms: named (&middot;, &hellip;) and numeric,
// decimal or hex (&#10003;, &#x2713;). \w covers the whole named-entity
// alphabet; the numeric forms add # and, for hex, x.
var entityRe = regexp.MustCompile(`&#?x?[0-9a-zA-Z]+;`)

func TestNoServedPanelContainsAnHTMLEntity(t *testing.T) {
	err := fs.WalkDir(assets, "static/js", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		b, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range entityRe.FindAllString(string(b), -1) {
			t.Errorf("%s contains %q -- htm never decodes entities; use the real "+
				"Unicode character in the JS source instead", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
