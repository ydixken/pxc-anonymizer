package buildconfig

import (
	"regexp"
	"strings"
	"testing"
)

func TestStrategyReferenceCoversAPIEnum(t *testing.T) {
	source := buildRead(t, "api/v1alpha1/strategies.go")
	marker := regexp.MustCompile(`(?m)^// \+kubebuilder:validation:Enum=([^\r\n]+)\r?\ntype Strategy string$`)
	matches := marker.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		t.Fatalf("Strategy enum markers = %d, want exactly one nonempty marker", len(matches))
	}
	names := strings.Split(matches[0][1], ";")
	document := buildRead(t, "docs/reference/strategies.md")
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || strings.TrimSpace(name) != name || seen[name] {
			t.Fatalf("empty, malformed or duplicate canonical strategy %q", name)
		}
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			heading := "\n## " + name + "\n"
			if count := strings.Count(document, heading); count != 1 {
				t.Fatalf("canonical strategy %q has %d reference headings, want one", name, count)
			}
		})
	}
	if !t.Failed() {
		t.Logf("Verified %d unique canonical API strategy headings.", len(seen))
	}
}
