package ui

import "testing"

// A locale-sensitive test has to STATE the locale, not inherit it.
//
// utf8Locale reads LC_ALL, then LC_CTYPE, then LANG in POSIX precedence order
// and returns on the FIRST one that is set, so setting only LANG leaves the
// answer to whatever the runner exports: a macOS runner exports LC_CTYPE, a
// C-locale runner exports LC_ALL, and either one decides the result before the
// test's own value is ever read. That cost a full CI cycle on OR-189, where a
// LANG=C test asserting the ASCII fallback passed on Linux and Windows and
// failed only on macOS.
//
// These two set all three together so the whole-locale requirement is the
// default rather than something to remember. Reach for them instead of
// t.Setenv("LANG", ...).

// setUTF8Locale makes the glyph path the answer regardless of the runner.
func setUTF8Locale(t *testing.T) {
	t.Helper()
	setLocale(t, "en_US.UTF-8")
}

// setASCIILocale makes the ASCII fallback the answer regardless of the runner.
func setASCIILocale(t *testing.T) {
	t.Helper()
	setLocale(t, "C")
}

func setLocale(t *testing.T, value string) {
	t.Helper()
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		t.Setenv(v, value)
	}
}

// The helpers exist to survive a hostile environment; a helper that sets only
// the lowest-precedence variable would pass under a clean env and fail on the
// runner that motivated it. Set the opposite locale first, in every slot, so
// only setting all three can produce the expected answer.
func TestTheLocaleHelpersOverrideEveryPrecedenceSlot(t *testing.T) {
	t.Run("utf8 over an exported C locale", func(t *testing.T) {
		setLocale(t, "C")
		setUTF8Locale(t)
		if !utf8Locale() {
			t.Error("setUTF8Locale did not reach the deciding variable")
		}
	})

	t.Run("ascii over an exported UTF-8 locale", func(t *testing.T) {
		setLocale(t, "en_US.UTF-8")
		setASCIILocale(t)
		if utf8Locale() {
			t.Error("setASCIILocale did not reach the deciding variable")
		}
	})

	// Each variable on its own: a runner that exports only LC_CTYPE (macOS) or
	// only LC_ALL still has to lose to the helper.
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		t.Run("utf8 over an exported "+v, func(t *testing.T) {
			setLocale(t, "")
			t.Setenv(v, "C")
			setUTF8Locale(t)
			if !utf8Locale() {
				t.Errorf("an exported %s decided the result instead of the test", v)
			}
		})
		t.Run("ascii over an exported "+v, func(t *testing.T) {
			setLocale(t, "")
			t.Setenv(v, "en_US.UTF-8")
			setASCIILocale(t)
			if utf8Locale() {
				t.Errorf("an exported %s decided the result instead of the test", v)
			}
		})
	}
}
