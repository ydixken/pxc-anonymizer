package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	commandTestHelp = "--help"
	commandTestSeed = "seed-demo"
)

func TestAnonymizeSubcommandStartsWithoutClusterConfiguration(t *testing.T) {
	const childMarker = "PXC_ANONYMIZER_TEST_DISPATCH"
	if os.Getenv(childMarker) == "1" {
		os.Args = []string{os.Args[0], "anonymize", commandTestHelp}
		main()
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAnonymizeSubcommandStartsWithoutClusterConfiguration$")
	command.Env = append(os.Environ(), childMarker+"=1", "KUBECONFIG="+t.TempDir()+"/absent")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("anonymize help: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "-reference-time") || strings.Contains(string(output), "-leader-elect") {
		t.Fatalf("subcommand did not reach the anonymization parser: %s", output)
	}
}

func TestCommandDispatch(t *testing.T) {
	const marker = "PXC_ANONYMIZER_TEST_COMMAND"
	cases := []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{"implicit-manager", []string{commandTestHelp}, "-watch-namespaces", false},
		{"explicit-manager", []string{"manager", commandTestHelp}, "-watch-namespaces", false},
		{commandTestSeed, []string{commandTestSeed, commandTestHelp}, "-scale", false},
		{"unknown-command", []string{"unknown"}, "expected manager, anonymize or seed-demo", true},
		{"manager-positional", []string{"manager", "unexpected"}, "manager accepts flags only", true},
		{"implicit-positional", []string{"--watch-namespaces=team-a", "unexpected"}, "manager accepts flags only", true},
	}
	for _, tc := range cases {
		if os.Getenv(marker) == tc.name {
			os.Args = append([]string{os.Args[0]}, tc.args...)
			main()
			return
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandDispatch$")
			command.Env = append(os.Environ(), marker+"="+tc.name, "KUBECONFIG="+t.TempDir()+"/absent")
			output, err := command.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(output), tc.want) {
				t.Fatalf("dispatch error = %v, output = %s", err, output)
			}
			if tc.name == commandTestSeed && strings.Contains(string(output), "-leader-elect") {
				t.Fatal("seed command reached the manager parser")
			}
		})
	}
}

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
			options, err := managerOptions(tc.namespaces)
			if (err != nil) != tc.invalid {
				t.Fatalf("managerOptions() error = %v, invalid = %v", err, tc.invalid)
			}
			if tc.invalid {
				return
			}
			if options.Scheme != scheme {
				t.Fatal("manager options lost the API scheme")
			}
			if len(options.Cache.DefaultNamespaces) != len(tc.want) {
				t.Fatalf("namespace count = %d, want %d", len(options.Cache.DefaultNamespaces), len(tc.want))
			}
			for _, name := range tc.want {
				if _, exists := options.Cache.DefaultNamespaces[name]; !exists {
					t.Errorf("missing watched namespace %q", name)
				}
			}
			if tc.namespaces == "" && options.Cache.DefaultNamespaces != nil {
				t.Error("cluster-wide watch must use the default cache")
			}
		})
	}
}
