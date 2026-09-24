package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// bareCollections lists the collection names a manifest introduces into the
// commons — bare names, family reservations included. Owned (`plugin.<id>.`)
// and platform names cannot collide and are left out.
func bareCollections(m *PluginManifest) []string {
	if m.Provides == nil {
		return nil
	}
	var out []string
	for name := range m.Provides.Collections {
		if strings.HasPrefix(name, "plugin.") || strings.HasPrefix(name, "_platform.") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// namespaceWarnings names every bare collection this manifest introduces
// that a listed plugin already introduces. A bare name is the commons —
// arbitrated by each user's ownership ledger once both are installed — so
// this is a warning, never a refusal: the author may mean the commons. The
// failure it exists for is ignorance, not intent: inventing a rival
// vocabulary having never seen the existing one.
func namespaceWarnings(m *PluginManifest, cat *catalog) []string {
	mine := bareCollections(m)
	if len(mine) == 0 || cat == nil {
		return nil
	}
	var warns []string
	for _, name := range mine {
		for _, e := range cat.Plugins {
			if e.ID == m.ID {
				continue
			}
			for _, c := range e.Collections {
				if c == name {
					warns = append(warns, fmt.Sprintf(
						"collection '%s' is already introduced by '%s' (%s). A bare name is the "+
							"commons: both plugins will claim it and each user decides who owns it. "+
							"If this is %s's own data, name it plugin.%s.%s",
						name, e.ID, e.Tier, m.ID, m.ID, name))
				}
			}
		}
	}
	return warns
}

// reportNamespace runs namespaceWarnings against the live catalog and prints
// the result to stderr. Best effort: an unreachable catalog is said once and
// the check skipped — never a reason to block a build.
func reportNamespace(m *PluginManifest) {
	if len(bareCollections(m)) == 0 {
		return
	}
	cat, err := fetchCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: catalog unreachable (%v); shared-name check skipped\n", err)
		return
	}
	for _, w := range namespaceWarnings(m, &cat) {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}
