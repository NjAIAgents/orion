package creds

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Wizard walks every credential, showing what is already set and offering to
// change it. Pressing enter keeps the current value, so re-running it to
// change one thing does not mean retyping the rest.
//
// It refuses to run without a terminal. A wizard invoked from cron would
// block forever on a read that never returns, which presents as a hung job
// rather than a misconfiguration.
func Wizard(home string, in io.Reader, out io.Writer, only []string) error {
	if !Interactive() {
		return fmt.Errorf("orion config needs a terminal.\n" +
			"  Run it from a shell, or write ~/.orion/config.env by hand.")
	}

	current, err := Load(home)
	if err != nil {
		fmt.Fprintf(out, "warning: existing config unreadable (%v); starting fresh\n\n", err)
	}

	fmt.Fprintf(out, "Orion configuration\n")
	fmt.Fprintf(out, "  file: %s (mode 0600, never committed)\n", Path(home))
	fmt.Fprintf(out, "  Read by the orion binary itself, so it works under cron and launchd\n")
	fmt.Fprintf(out, "  where a shell profile would not.\n\n")
	fmt.Fprintf(out, "  Enter keeps the current value. Everything here is optional.\n\n")

	keys := Known
	if len(only) > 0 {
		keys = only
	}

	updates := map[string]string{}
	for _, k := range keys {
		// An environment override would make anything stored here invisible,
		// so say that at the point of entry rather than letting someone
		// wonder why their new value had no effect.
		if envv := strings.TrimSpace(os.Getenv(k)); envv != "" {
			shown := envv
			if Secret(k) {
				shown = Mask(envv)
			}
			fmt.Fprintf(out, "%s\n  NOTE: %s is set in your environment (%s).\n"+
				"  That wins over this file, so a value set here will not take effect\n"+
				"  until you unset it.\n\n", Label(k), k, shown)
		}

		v, err := Prompt(in, out, Label(k), current[k], Secret(k))
		if err != nil && v == "" {
			return err
		}
		if v != current[k] {
			updates[k] = v
		}
		fmt.Fprintln(out)
	}

	if len(updates) == 0 {
		fmt.Fprintln(out, "nothing changed.")
		return nil
	}
	if err := Save(home, updates); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %d value(s) to %s\n", len(updates), Path(home))
	fmt.Fprintln(out, "\nVerify what Orion can actually do with them:  orion doctor")
	return nil
}

// Show lists the configuration without revealing secrets, and says where
// each value came from. The source matters: "set but ignored because the
// environment overrides it" is a real state people land in.
func Show(home string, out io.Writer) error {
	fmt.Fprintf(out, "file  %s\n", Path(home))

	if okPerm, mode, err := CheckPerms(home); err == nil {
		if !okPerm {
			fmt.Fprintf(out, "mode  %o  TOO OPEN. Anyone on this machine can read your tokens.\n", mode)
			fmt.Fprintf(out, "      Fix with: chmod 600 %s\n", Path(home))
		} else {
			fmt.Fprintf(out, "mode  %o\n", mode)
		}
	} else if os.IsNotExist(err) {
		fmt.Fprintln(out, "      (does not exist yet; run: orion config)")
	}
	fmt.Fprintln(out)

	for _, k := range Known {
		v := Get(home, k)
		if v == "" {
			fmt.Fprintf(out, "  %-22s (not set)\n", k)
			continue
		}
		shown := v
		if Secret(k) {
			shown = Mask(v)
		}
		fmt.Fprintf(out, "  %-22s %-28s from %s\n", k, shown, Source(home, k))
	}
	return nil
}
