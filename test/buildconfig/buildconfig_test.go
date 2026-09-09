package buildconfig

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const buildExpectedOutput = "sibling:v9.8.7"

func buildRead(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func buildWrite(t *testing.T, directory, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func buildFixture(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	buildWrite(t, directory, "Makefile", buildRead(t, "Makefile"), 0o600)
	buildWrite(t, directory, "go.mod", "module example.test/buildfixture\n\ngo 1.26.0\n", 0o600)
	buildWrite(t, directory, "cmd/main.go", `package main
import "fmt"
var version = "dev"
func main() { fmt.Printf("%s:%s\n", buildSibling(), version) }
`, 0o600)
	buildWrite(t, directory, "cmd/sibling.go", `package main
func buildSibling() string { return "sibling" }
`, 0o600)
	return directory
}

func buildCommand(t *testing.T, directory string, environment []string, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
	command.Env = append(command.Env, environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func buildMake(
	t *testing.T, directory string, environment []string, target string, variables ...string,
) (string, error) {
	t.Helper()
	args := make([]string, 0, 11+len(variables))
	args = append(args, "--no-print-directory", "-s", "-o", "manifests", "-o", "generate",
		"-o", "fmt", "-o", "vet", target)
	args = append(args, variables...)
	return buildCommand(t, directory, environment, "make", args...)
}

func buildRequireCommand(t *testing.T, directory string, environment []string, name string, args ...string) string {
	t.Helper()
	output, err := buildCommand(t, directory, environment, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return strings.TrimSpace(output)
}

func TestBuildCompilesSiblingFilesAndStampsVersion(t *testing.T) {
	directory := buildFixture(t)
	output, err := buildMake(t, directory, nil, "build", "VERSION=v9.8.7")
	if err != nil {
		t.Fatalf("make build: %v\n%s", err, output)
	}
	got := buildRequireCommand(t, directory, nil, filepath.Join(directory, "bin", "manager"))
	if got != buildExpectedOutput {
		t.Fatalf("built fixture printed %q", got)
	}
}

func TestBuildRunCompilesSiblingFilesAndStampsVersion(t *testing.T) {
	output, err := buildMake(t, buildFixture(t), nil, "run", "VERSION=v9.8.7")
	if err != nil || strings.TrimSpace(output) != buildExpectedOutput {
		t.Fatalf("make run: %v, output %q", err, output)
	}
}

func TestBuildVersionSupportsUnbornAndTaggedRepositories(t *testing.T) {
	directory := buildFixture(t)
	buildRequireCommand(t, directory, nil, "git", "init", "-b", "feat/build-fixture")
	for _, version := range []string{"dev", "v9.8.7"} {
		if version != "dev" {
			buildRequireCommand(t, directory, nil, "git", "add", "Makefile", "go.mod", "cmd")
			buildRequireCommand(t, directory, nil, "git", "-c", "user.name=Build fixture", "-c",
				"user.email=build@example.test", "-c", "commit.gpgsign=false", "commit", "-m", "test: build fixture")
			buildRequireCommand(t, directory, nil, "git", "tag", version)
		}
		output, err := buildMake(t, directory, nil, "build")
		if err != nil || strings.Contains(output, "fatal:") {
			t.Fatalf("make build with %s metadata: %v\n%s", version, err, output)
		}
		got := buildRequireCommand(t, directory, nil, filepath.Join(directory, "bin", "manager"))
		if got != "sibling:"+version {
			t.Fatalf("with %s metadata: got %q", version, got)
		}
	}
}

func TestEnvtestVersionsFollowRequiredAndReplacedModules(t *testing.T) {
	for _, test := range []struct {
		name, replacement, want string
	}{
		{name: "required", want: "v0.20.2|1.32"},
		{name: "replaced", replacement: "replace sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.21.1\n" +
			"replace k8s.io/api => k8s.io/api v0.33.2\n", want: "v0.21.1|1.33"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := buildFixture(t)
			module := "module example.test/buildfixture\n\ngo 1.26.0\n" +
				"require (\n sigs.k8s.io/controller-runtime v0.20.2\n k8s.io/api v0.32.3\n)\n" + test.replacement
			buildWrite(t, directory, "go.mod", module, 0o600)
			for modulePath, version := range map[string]string{
				"sigs.k8s.io/controller-runtime": "v0.20.2", "k8s.io/api": "v0.32.3",
			} {
				prefix := filepath.Join("build-proxy", modulePath, "@v", version)
				buildWrite(t, directory, prefix+".mod", "module "+modulePath+"\n\ngo 1.26.0\n", 0o600)
				buildWrite(t, directory, prefix+".info",
					`{"Version":"`+version+`","Time":"2026-01-01T00:00:00Z"}`, 0o600)
			}
			buildWrite(t, directory, "build-print.mk", "include Makefile\n"+
				"build-print:\n\t@printf '%s|%s\\n' '$(ENVTEST_VERSION)' '$(ENVTEST_K8S_VERSION)'\n", 0o600)
			environment := []string{"GOPROXY=file://" + filepath.Join(directory, "build-proxy"),
				"GOMODCACHE=" + filepath.Join(directory, "build-modcache"), "GOSUMDB=off"}
			got := buildRequireCommand(t, directory, environment, "make", "-s", "-f", "build-print.mk", "build-print")
			if got != test.want {
				t.Fatalf("derived envtest versions: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestToolInstallerReusesAndSwitchesVersionedSymlinks(t *testing.T) {
	directory := buildFixture(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	buildWrite(t, directory, "build-tools/go", `#!/bin/sh
set -eu
if [ "$1" != install ]; then exec "$BUILD_REAL_GO" "$@"; fi
case "$2" in example.test/tools/build-tool@v1.0.0|example.test/tools/build-tool@v1.0.1) ;; *) exit 64 ;; esac
printf '%s\n' "$2" >> "$BUILD_INSTALL_LOG"
printf '#!/bin/sh\nprintf "%%s\\n" "%s"\n' "${2##*@}" > "$GOBIN/build-tool"
chmod +x "$GOBIN/build-tool"
`, 0o755)
	buildWrite(t, directory, "build-install.mk", "include Makefile\n.PHONY: build-install\n"+
		"build-install: | $(LOCALBIN)\n"+
		"\t$(call go-install-tool,$(LOCALBIN)/build-tool,example.test/tools/build-tool,$(BUILD_TOOL_VERSION))\n", 0o600)
	toolPath := filepath.Join(directory, "build-tools") + string(os.PathListSeparator) + os.Getenv("PATH")
	environment := []string{"PATH=" + toolPath,
		"BUILD_REAL_GO=" + realGo, "BUILD_INSTALL_LOG=" + filepath.Join(directory, "build-install.log")}
	for _, version := range []string{"v1.0.0", "v1.0.0", "v1.0.1"} {
		buildRequireCommand(t, directory, environment, "make", "-s", "-f", "build-install.mk",
			"build-install", "BUILD_TOOL_VERSION="+version)
		link := filepath.Join(directory, "bin", "build-tool")
		target, linkErr := os.Readlink(link)
		if linkErr != nil || target != link+"-"+version {
			t.Fatalf("tool link: target %q, error %v", target, linkErr)
		}
		if got := buildRequireCommand(t, directory, nil, link); got != version {
			t.Fatalf("tool version: got %q, want %q", got, version)
		}
	}
	log, err := os.ReadFile(filepath.Join(directory, "build-install.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(log)); !slices.Equal(got,
		[]string{"example.test/tools/build-tool@v1.0.0", "example.test/tools/build-tool@v1.0.1"}) {
		t.Fatalf("unexpected installations: %v", got)
	}
}

func TestDockerBuildCommandCompilesSiblingFilesAndStampsVersion(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(buildRead(t, "Dockerfile")))
	var commands []string
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "RUN ") && strings.Contains(line, "go build") {
			commands = append(commands, strings.TrimPrefix(line, "RUN "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected one Docker build command, got %d", len(commands))
	}
	directory := buildFixture(t)
	environment := []string{"TARGETOS=" + runtime.GOOS, "TARGETARCH=" + runtime.GOARCH, "VERSION=v9.8.7"}
	buildRequireCommand(t, directory, environment, "sh", "-ec", commands[0])
	got := buildRequireCommand(t, directory, nil, filepath.Join(directory, "manager"))
	if got != buildExpectedOutput {
		t.Fatalf("Docker build fixture printed %q", got)
	}
}

func TestDockerBuildReceivesVersionArgument(t *testing.T) {
	directory := buildFixture(t)
	buildWrite(t, directory, "build-container", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$BUILD_CONTAINER_LOG\"\n", 0o755)
	environment := []string{"BUILD_CONTAINER_LOG=" + filepath.Join(directory, "build-container.log")}
	output, err := buildMake(t, directory, environment, "docker-build", "VERSION=v9.8.7",
		"IMG=example.test/build:v9.8.7", "CONTAINER_TOOL="+filepath.Join(directory, "build-container"))
	if err != nil {
		t.Fatalf("make docker-build: %v\n%s", err, output)
	}
	log, err := os.ReadFile(filepath.Join(directory, "build-container.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "--build-arg", "VERSION=v9.8.7", "-t", "example.test/build:v9.8.7", "."}
	if got := strings.Fields(string(log)); !slices.Equal(got, want) {
		t.Fatalf("container arguments: got %v, want %v", got, want)
	}
}

func TestDockerBuildxUsesNativeBuilderOnce(t *testing.T) {
	directory := buildFixture(t)
	buildWrite(t, directory, "Dockerfile", buildRead(t, "Dockerfile"), 0o600)
	buildWrite(t, directory, "build-container", `#!/bin/sh
set -eu
if [ "$1" = buildx ] && [ "$2" = build ]; then
  printf '%s\n' "$@" > "$BUILD_CONTAINER_LOG"
  dockerfile=Dockerfile
  shift 2
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -f ]; then shift; dockerfile=$1; fi
    shift
  done
  cp "$dockerfile" "$BUILD_DOCKERFILE_CAPTURE"
fi
`, 0o755)
	logPath := filepath.Join(directory, "build-container.log")
	capturePath := filepath.Join(directory, "build-Dockerfile.capture")
	environment := []string{"BUILD_CONTAINER_LOG=" + logPath, "BUILD_DOCKERFILE_CAPTURE=" + capturePath}
	output, err := buildMake(t, directory, environment, "docker-buildx", "IMG=example.test/build:v9.8.7",
		"PLATFORMS=linux/arm64,linux/amd64", "CONTAINER_TOOL="+filepath.Join(directory, "build-container"))
	if err != nil {
		t.Fatalf("make docker-buildx: %v\n%s", err, output)
	}
	dockerfile, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var builder string
	for line := range strings.Lines(string(dockerfile)) {
		if strings.HasPrefix(line, "FROM ") {
			builder = line
			break
		}
	}
	if strings.Count(builder, "--platform=") != 1 || !strings.Contains(builder, "--platform=$BUILDPLATFORM ") {
		t.Fatalf("expected one native builder platform argument, got %q", builder)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"buildx", "build", "--push", "--platform=linux/arm64,linux/amd64", "--tag",
		"example.test/build:v9.8.7", "."}
	if got := strings.Fields(string(log)); !slices.Equal(got, want) {
		t.Fatalf("container arguments: got %v, want %v", got, want)
	}
}

func TestBuildCustomLinterFailsClosed(t *testing.T) {
	directory := buildFixture(t)
	buildWrite(t, directory, ".custom-gcl.yml", "version: v2.13.2\n", 0o600)
	buildWrite(t, directory, "bin/golangci-lint", `#!/bin/sh
printf '%s\n' "$*" >> "$BUILD_LINT_LOG"
exit 23
`, 0o755)
	buildWrite(t, directory, "build-lint.mk", "include Makefile\n"+
		"define go-install-tool\n@:\nendef\n", 0o600)
	logPath := filepath.Join(directory, "build-lint.log")
	output, err := buildCommand(t, directory, []string{"BUILD_LINT_LOG=" + logPath},
		"make", "-B", "-s", "-f", "build-lint.mk", "golangci-lint")
	if err == nil {
		t.Fatalf("custom linter failure was accepted:\n%s", output)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil || !strings.HasPrefix(string(log), "custom --destination ") {
		t.Fatalf("custom build was not invoked: %v, log %q", readErr, log)
	}
}
