package buildconfig

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The stub records the CLI boundary; the launcher must never need a live cluster in tests.
const (
	demoTmuxCommand       = "tmux"
	demoContextFlag       = "--context"
	demoNamespaceFlag     = "--namespace"
	demoSourceNamespace   = "source-demo"
	demoPipelineNamespace = "pipeline-demo"
	demoFixtureName       = "demo"
)

const demoTmuxStub = `#!/bin/bash
printf '%s\0' "$@" >> "$DEMO_TEST_CALLS"
printf '\0' >> "$DEMO_TEST_CALLS"
case "$1" in
  has-session) test "${DEMO_TEST_EXISTS:-}" = true ;;
  show-options) printf '%s' "${DEMO_TEST_MARKER:-}" ;;
  new-session) printf '%%0\n' ;;
  split-window)
    test "${DEMO_TEST_FAIL_SPLIT:-}" != true || exit 42
    printf '%%1\n' ;;
  list-panes) printf '0\n0\n0\n0\n' ;;
esac
`

func demoSessionFixture(t *testing.T, tools ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		body := "#!/bin/bash\nexit 0\n"
		if tool == demoTmuxCommand {
			body = demoTmuxStub
		}
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir, filepath.Join(dir, "calls")
}

func demoSessionCommand(t *testing.T, dir string, environment []string, args ...string) (string, [][]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	script := filepath.Join("..", "..", "scripts", "demo-session.sh")
	command := exec.CommandContext(ctx, "/bin/bash", append([]string{script}, args...)...)
	command.Env = append([]string{
		"PATH=" + dir, "DEMO_TEST_CALLS=" + filepath.Join(dir, "calls"), "TMUX=",
	}, environment...)
	output, err := command.CombinedOutput()
	data, readErr := os.ReadFile(filepath.Join(dir, "calls"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	var calls [][]string
	for record := range strings.SplitSeq(string(data), "\x00\x00") {
		if record != "" {
			calls = append(calls, strings.Split(record, "\x00"))
		}
	}
	return string(output), calls, err
}

func demoSessionArgs(contextName string) []string {
	return []string{
		demoContextFlag, contextName, "--source-namespace", demoSourceNamespace,
		"--operator-namespace", demoPipelineNamespace, "--downstream-namespace", "downstream-demo", "--detach",
	}
}

func TestDemoSessionRejectsInvalidInputsAndMissingTools(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		tools []string
		want  string
	}{
		{name: "missing context", want: "--context is required"},
		{name: "missing value", args: []string{demoContextFlag}, want: "requires a value"},
		{name: "missing namespaces", args: []string{demoContextFlag, demoFixtureName}, want: "All three namespaces"},
		{name: "tmux unavailable", args: demoSessionArgs(demoFixtureName), want: "tmux is required"},
		{name: "k9s unavailable", args: demoSessionArgs(demoFixtureName),
			tools: []string{demoTmuxCommand}, want: "k9s is required"},
		{name: "tmux separator", args: demoSessionArgs("demo;"), want: "semicolons or control characters"},
		{name: "context control byte", args: demoSessionArgs("demo\nnext"), want: "semicolons or control characters"},
		{name: "session targeting syntax",
			args: append(demoSessionArgs(demoFixtureName), "--session", "other:window"), want: "Session name"},
		{name: "namespace syntax",
			args: append(demoSessionArgs(demoFixtureName), "--source-namespace", "Bad"), want: "All three namespaces"},
		{name: "unknown option", args: []string{"--unknown"}, want: "Unknown option"},
		{name: "attach without terminal", args: demoSessionArgs(demoFixtureName)[:8],
			tools: []string{demoTmuxCommand, "k9s"}, want: "requires a terminal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := demoSessionFixture(t, tt.tools...)
			output, calls, err := demoSessionCommand(t, dir, nil, tt.args...)
			if err == nil || !strings.Contains(output, tt.want) || len(calls) != 0 {
				t.Fatalf("error=%v output=%q calls=%v; want rejection before tmux for %q", err, output, calls, tt.want)
			}
		})
	}
}

func TestDemoSessionArgumentsAndSafeReuse(t *testing.T) {
	dir, callsPath := demoSessionFixture(t, demoTmuxCommand, "k9s")
	injected := filepath.Join(dir, "injected")
	contextName := "demo 'quoted' \"context\" $(printf bad > " + injected + ")"
	output, calls, err := demoSessionCommand(t, dir, nil, demoSessionArgs(contextName)...)
	if err != nil {
		t.Fatalf("launch: %v: %s", err, output)
	}
	if !strings.Contains(output, "tmux -CC attach-session -t =pxc-demo") {
		t.Fatalf("detached setup omitted native iTerm2 attachment: %q", output)
	}
	wantViews := map[string]string{
		"perconaxtradbclusterbackups": demoSourceNamespace, "anonymizationruns": demoPipelineNamespace,
		"events": demoPipelineNamespace, "bootstraps": "downstream-demo",
	}
	marker := ""
	for _, call := range calls {
		if i := slices.Index(call, filepath.Join(dir, "k9s")); i >= 0 {
			args := call[i+1:]
			if end := slices.Index(args, ";"); end >= 0 {
				args = args[:end]
			}
			if len(args) != 9 {
				t.Fatalf("unexpected k9s argv: %q", args)
			}
			resource := args[8]
			namespace, ok := wantViews[resource]
			want := []string{
				"--readonly", demoContextFlag, contextName, "--logoless", "--splashless",
				demoNamespaceFlag, namespace, "--command", resource,
			}
			if !ok || !slices.Equal(args, want) {
				t.Fatalf("k9s argv=%q, want %q", args, want)
			}
			delete(wantViews, resource)
		}
		if i := slices.Index(call, "@pxc-demo-config"); call[0] == "set-option" && i >= 0 {
			marker = call[i+1]
		}
	}
	if len(wantViews) != 0 || marker == "" {
		t.Fatalf("missing views=%v or completion marker=%q", wantViews, marker)
	}
	if _, err := os.Stat(injected); !os.IsNotExist(err) {
		t.Fatalf("context executed as shell code: %v", err)
	}
	for _, tt := range []struct {
		name   string
		marker string
		ok     bool
	}{
		{name: "matching session", marker: marker, ok: true},
		{name: "unrelated session"},
		{name: "different configuration", marker: "another configuration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(callsPath); err != nil {
				t.Fatal(err)
			}
			environment := []string{"DEMO_TEST_EXISTS=true", "DEMO_TEST_MARKER=" + tt.marker}
			output, calls, err := demoSessionCommand(t, dir, environment, demoSessionArgs(contextName)...)
			if (err == nil) != tt.ok || len(calls) != 2 || calls[0][0] != "has-session" || calls[1][0] != "show-options" {
				t.Fatalf("rerun mutated session or wrong result: error=%v output=%q calls=%v", err, output, calls)
			}
		})
	}
}

func TestDemoSessionNativeITermAttachment(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "demo-session.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, `exec tmux -CC attach-session -t "$target"`) ||
		strings.Contains(source, "exec tmux attach-session") || strings.Contains(source, "switch-client") {
		t.Fatal("interactive launcher must attach in native iTerm2 control mode without a regular tmux fallback")
	}
}

func TestDemoSessionPartialFailurePreservesPanes(t *testing.T) {
	dir, _ := demoSessionFixture(t, demoTmuxCommand, "k9s")
	environment := []string{"DEMO_TEST_FAIL_SPLIT=true"}
	output, calls, err := demoSessionCommand(t, dir, environment, demoSessionArgs(demoFixtureName)...)
	if err == nil || !strings.Contains(output, "Setup failed") || strings.Contains(output, "is available") {
		t.Fatalf("partial setup reported success: error=%v output=%q", err, output)
	}
	for _, call := range calls {
		if strings.HasPrefix(call[0], "kill-") || (call[0] == "set-option" && slices.Contains(call, "@pxc-demo-config")) {
			t.Fatalf("partial failure destroyed a session or marked it complete: %v", call)
		}
	}
}
