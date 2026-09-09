package buildconfig

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	secrecySentinel             = "publication-sentinel.example.invalid"
	secrecyForbiddenContent     = "forbidden content found"
	secrecyCIEnabled            = "true"
	secrecyLocalDenyListMissing = "local deny-list missing"
)

func TestSecrecyGuard(t *testing.T) {
	tests := []struct {
		name  string
		ci    string
		setup func(*testing.T, string)
		want  string
	}{
		{name: "clean untracked candidate"},
		{name: "untracked sentinel", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.txt", secrecySentinel)
		}, want: secrecyForbiddenContent},
		{name: "case and URL punctuation", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.txt", "See https://"+strings.ToUpper(secrecySentinel)+"/backup.")
		}, want: secrecyForbiddenContent},
		{name: "candidate filename", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, secrecySentinel+".txt", "clean content")
		}, want: secrecyForbiddenContent},
		{name: "tracked working tree", setup: func(t *testing.T, dir string) {
			secrecyGit(t, dir, "add", "candidate.txt")
			secrecyWrite(t, dir, "candidate.txt", secrecySentinel)
		}, want: secrecyForbiddenContent},
		{name: "staged secret hidden by clean working tree", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.txt", secrecySentinel)
			secrecyGit(t, dir, "add", "candidate.txt")
			secrecyWrite(t, dir, "candidate.txt", "clean content")
		}, want: secrecyForbiddenContent},
		{name: "binary content", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.bin", "\x00"+secrecySentinel+"\x00")
		}, want: secrecyForbiddenContent},
		{name: "ignored private notes", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "tasks/private.md", secrecySentinel)
		}},
		{name: "spaces and newlines in paths", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "a space\nand newline.txt", secrecySentinel)
		}, want: secrecyForbiddenContent},
		{name: "missing digest", ci: secrecyCIEnabled, setup: func(t *testing.T, dir string) {
			secrecyRemove(t, dir, "hack/secrecy-deny.sha256")
		}, want: "digest deny-list missing"},
		{name: "empty digest", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "hack/secrecy-deny.sha256", "\n")
		}, want: "digest deny-list missing"},
		{name: "invalid digest", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "hack/secrecy-deny.sha256", "not-a-digest\n")
		}, want: "digest deny-list invalid"},
		{name: "missing local deny list", setup: func(t *testing.T, dir string) {
			secrecyRemove(t, dir, "tasks/secrecy-deny.regex")
		}, want: secrecyLocalDenyListMissing},
		{name: "empty local deny list", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "tasks/secrecy-deny.regex", "\n")
		}, want: secrecyLocalDenyListMissing},
		{name: "invalid local regex", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "tasks/secrecy-deny.regex", "[\n")
		}, want: "local deny-list invalid"},
		{name: "local regex detection", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.txt", "private-fixture-42")
		}, want: secrecyForbiddenContent},
		{name: "CI skips local list", ci: secrecyCIEnabled, setup: func(t *testing.T, dir string) {
			secrecyRemove(t, dir, "tasks/secrecy-deny.regex")
		}},
		{name: "CI keeps digest detection", ci: secrecyCIEnabled, setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, "candidate.txt", secrecySentinel)
		}, want: secrecyForbiddenContent},
		{name: "other CI values retain local check", ci: "1", setup: func(t *testing.T, dir string) {
			secrecyRemove(t, dir, "tasks/secrecy-deny.regex")
		}, want: secrecyLocalDenyListMissing},
		{name: "missing tracked file", setup: func(t *testing.T, dir string) {
			secrecyGit(t, dir, "add", "candidate.txt")
			secrecyRemove(t, dir, "candidate.txt")
		}, want: "candidate unreadable"},
		{name: "no publication candidates", setup: func(t *testing.T, dir string) {
			secrecyWrite(t, dir, ".gitignore", "*\n")
		}, want: "no publication candidates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := secrecyFixture(t)
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			cmd := exec.Command("sh", filepath.Join("..", "..", "hack", "check-secrecy.sh"))
			guard, err := filepath.Abs(cmd.Args[1])
			if err != nil {
				t.Fatal(err)
			}
			cmd.Args[1] = guard
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CI="+tt.ci)
			output, err := cmd.CombinedOutput()
			if tt.want == "" {
				if err != nil || !strings.Contains(string(output), "secrecy: PASS") {
					t.Fatalf("clean candidate rejected: %v; %s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tt.want) {
				t.Fatalf("wanted %q failure; got %v; %s", tt.want, err, output)
			}
			if strings.Contains(strings.ToLower(string(output)), secrecySentinel) ||
				strings.Contains(string(output), "private-fixture-42") {
				t.Fatal("guard printed matched content")
			}
		})
	}
}

func secrecyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	secrecyGit(t, dir, "init", "-q", "-b", "secrecy-test")
	secrecyWrite(t, dir, ".gitignore", "/tasks/\n")
	secrecyWrite(t, dir, "hack/secrecy-deny.sha256", fmt.Sprintf("%x\n", sha256.Sum256([]byte(secrecySentinel))))
	secrecyWrite(t, dir, "tasks/secrecy-deny.regex", "private-fixture-[0-9]+\n")
	secrecyWrite(t, dir, "candidate.txt", "safe public example\n")
	return dir
}

func secrecyWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	filename := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func secrecyRemove(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func secrecyGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture git command failed: %v; %s", err, output)
	}
}
