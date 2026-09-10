package web

import (
	"testing"
	"time"
)

// These cases each defend one exported name or field the rest of the OR-55
// epic is written against. They are deliberately narrow -- one assertion
// apiece -- so a future rename of any single field fails exactly one test
// instead of the whole vocabulary at once.

func TestSnapshotTypeExported(t *testing.T) {
	var _ Snapshot = Snapshot{}
}

func TestCardTypeExported(t *testing.T) {
	var _ Card = Card{}
}

func TestSessionTypeExported(t *testing.T) {
	var _ Session = Session{}
}

func TestSnapshotAtIsTime(t *testing.T) {
	var _ time.Time = Snapshot{}.At
}

func TestSnapshotCardsIsCardSlice(t *testing.T) {
	var _ []Card = Snapshot{}.Cards
}

func TestCardKeyIsString(t *testing.T) {
	var _ string = Card{}.Key
}
