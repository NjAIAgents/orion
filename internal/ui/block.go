package ui

// Block boundaries: the opening and closing rule around output that is a
// BLOCK rather than a line.
//
// The same argument stage.go makes, one level up. Orion's console is a stream
// of one-line status records, interleaved from more than one run; anything
// structurally different from that -- a table, a report, a multi-paragraph
// summary -- has no edges of its own and simply runs into whatever printed
// before and after it. The cost report was the case that proved it: it began
// with a bare title line and ended with a wall-time sentence, so in a
// concurrent log there was nothing to say where it started or stopped
// (OR-219).
//
// So a block is distinguished by LAYOUT, exactly as a stage boundary is: a
// full-width rule above and below, which nothing else in this renderer
// produces.
//
// AND THE WORDS CARRY THE MEANING, per OR-163 and stage.go. Both rules
// contain the block's name in plain text and the closing one contains the
// literal word "end", so a piped log stays greppable and a reader who cannot
// see the rule still reads where the block finished. The glyph only
// reinforces, and it degrades to ASCII on NO_COLOR and a non-UTF-8 locale the
// same way every other glyph here does.
//
// No colour at all, deliberately. A block like the cost report is rendered
// once and sent to two sinks -- the terminal and a tracker comment -- and an
// escape code that reads as emphasis in one reads as line noise in the other.

import (
	"strings"
	"unicode/utf8"
)

const (
	blockRuleGlyph = "═"
	blockRuleASCII = "="
	// blockWidth is the column the rule is filled out to. Wide enough to look
	// like a boundary rather than a heading, narrow enough not to wrap the
	// 80-column terminal a status log is usually read in.
	blockWidth = 72
	// blockLead is how much rule precedes the title, so the line is
	// recognisable as a boundary from its first character.
	blockLead = 3
	// endWord is in the closing rule as a WORD so `grep end` finds where a
	// block finished in a log that has no colour and no box drawing.
	endWord = "end of"
)

func blockRule() string {
	if glyphs() {
		return blockRuleGlyph
	}
	return blockRuleASCII
}

// BlockStart returns the opening boundary of a named block.
func BlockStart(title string) string { return blockLine(title) }

// BlockEnd returns its closing boundary. Named, not bare, because two blocks
// interleaved in one log need to say WHICH one just ended.
func BlockEnd(title string) string { return blockLine(endWord + " " + title) }

func blockLine(title string) string {
	r := blockRule()
	head := strings.Repeat(r, blockLead) + " " + title + " "
	if n := blockWidth - utf8.RuneCountInString(head); n > 0 {
		return head + strings.Repeat(r, n)
	}
	// A title wider than the line still gets a closing rule, so the shape is
	// the same on a long ticket key as on a short one.
	return head + strings.Repeat(r, blockLead)
}
