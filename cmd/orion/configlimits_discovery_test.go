package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// The discovery ceiling is settable the same way every other round ceiling is,
// and no repository has ever carried a "discovery" block -- so the block is
// created rather than demanded. Answering "add a discovery block and re-run"
// sends the operator to hand-edit the file, which is the thing this command
// exists so nobody has to do.
func TestSettingTheDiscoveryCeilingIsReadBackByDiscovery(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	if err := configLimits(home, src, &out, []string{"discovery.max_rounds", "3"}); err != nil {
		t.Fatalf("configLimits: %v", err)
	}
	if got := config.Load(src).Discovery.Rounds(); got != 3 {
		t.Fatalf("Discovery.Rounds() = %d after setting it to 3", got)
	}
	// The previous value is the shipped default, said out loud: a person who
	// raises a ceiling should be able to read what they raised it from.
	if !strings.Contains(out.String(), "was 2") {
		t.Errorf("the previous value was not reported:\n%s", out.String())
	}
	body := readFile(t, src+"/orion.json")
	if !strings.Contains(blockBody(t, body, "discovery"), "3") {
		t.Errorf("max_rounds was not written into the discovery block:\n%s", body)
	}
}

// An unknown key names every bound this command can set. A ceiling that is
// invisible from the command that sets ceilings is one nobody finds.
func TestDiscoveryCeilingIsNamedAmongTheLimits(t *testing.T) {
	home := t.TempDir()
	src := registeredProject(t, home, "OR", adoptedConfig)

	var out bytes.Buffer
	err := configLimits(home, src, &out, []string{"no_such_limit", "3"})
	if err == nil {
		t.Fatal("an unknown key must be refused")
	}
	if !strings.Contains(err.Error()+out.String(), "discovery.max_rounds") {
		t.Errorf("discovery.max_rounds is not listed:\n%v\n%s", err, out.String())
	}
}
