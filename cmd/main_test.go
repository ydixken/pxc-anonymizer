package main

import "testing"

func TestManagerOptions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		namespaces string
		want       []string
		invalid    bool
	}{
		{name: "cluster wide"},
		{name: "scoped", namespaces: "team-a, team-b", want: []string{"team-a", "team-b"}},
		{name: "duplicate", namespaces: "team-a,team-a", want: []string{"team-a"}},
		{name: "empty entry", namespaces: "team-a,", invalid: true},
		{name: "invalid namespace", namespaces: "Team_A", invalid: true},
		{name: "whitespace only", namespaces: " ", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := namespaceCache(tc.namespaces)
			if (err != nil) != tc.invalid {
				t.Fatalf("namespaceCache() error = %v, invalid = %v", err, tc.invalid)
			}
			if tc.invalid {
				return
			}
			if len(options.DefaultNamespaces) != len(tc.want) {
				t.Fatalf("namespace count = %d, want %d", len(options.DefaultNamespaces), len(tc.want))
			}
			for _, name := range tc.want {
				if _, exists := options.DefaultNamespaces[name]; !exists {
					t.Errorf("missing watched namespace %q", name)
				}
			}
			if tc.namespaces == "" && options.DefaultNamespaces != nil {
				t.Error("cluster-wide watch must use the default cache")
			}
		})
	}
}
