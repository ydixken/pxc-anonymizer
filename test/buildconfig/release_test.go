package buildconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

const (
	releaseTestTag   = "v9.8.7"
	releaseTestImage = "ghcr.io/ydixken/pxc-anonymizer"
)

type releaseWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			ID   string `json:"id"`
			Uses string `json:"uses"`
			Run  string `json:"run"`
		} `json:"steps"`
	} `json:"jobs"`
}

func releaseReadWorkflow(t *testing.T, name string) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(buildRead(t, filepath.Join(".github", "workflows", name))), &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatalf("workflow %s has no jobs", name)
	}
	return workflow
}

func releaseStepCommand(t *testing.T, job, id string) string {
	t.Helper()
	var command string
	for _, step := range releaseReadWorkflow(t, "release.yml").Jobs[job].Steps {
		if step.ID == id {
			if command != "" || step.Run == "" {
				t.Fatalf("expected one nonempty %s/%s command", job, id)
			}
			command = step.Run
		}
	}
	if command == "" {
		t.Fatalf("missing release command %s/%s", job, id)
	}
	return command
}

func TestNoWorkflowInstallsQEMU(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	var workflows int
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		workflows++
		for job, spec := range releaseReadWorkflow(t, entry.Name()).Jobs {
			for _, step := range spec.Steps {
				content := strings.ToLower(step.Uses + "\n" + step.Run)
				if strings.Contains(content, "qemu") || strings.Contains(content, "binfmt") {
					t.Errorf("workflow %s job %s configures emulation", entry.Name(), job)
				}
			}
		}
	}
	if workflows == 0 {
		t.Fatal("no workflows inspected")
	}
}

func TestReleaseTagSelection(t *testing.T) {
	tagsCommand := releaseStepCommand(t, "manager-image", "tags")
	notesCommand := releaseStepCommand(t, "release-notes", "notes")
	for _, tag := range []string{releaseTestTag, releaseTestTag + "-alpha.1", releaseTestTag + "-rc.1"} {
		t.Run(tag, func(t *testing.T) {
			directory := t.TempDir()
			outputPath := filepath.Join(directory, "github-output")
			argsPath := filepath.Join(directory, "release-args")
			buildWrite(t, directory, "tools/gh", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RELEASE_TEST_ARGS\"\n", 0o755)
			environment := []string{
				"IMAGE=" + releaseTestImage, "GITHUB_REF_NAME=" + tag, "GITHUB_OUTPUT=" + outputPath,
				"RELEASE_TEST_ARGS=" + argsPath,
				"PATH=" + filepath.Join(directory, "tools") + string(os.PathListSeparator) + os.Getenv("PATH"),
			}
			buildRequireCommand(t, directory, environment, "bash", "-e", "-c", tagsCommand)
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "list<<EOF\n" + releaseTestImage + ":" + tag + "\n"
			if !strings.Contains(tag, "-") {
				want += releaseTestImage + ":latest\n"
			}
			if string(output) != want+"EOF\n" {
				t.Fatalf("image tags: got %q, want %q", output, want+"EOF\n")
			}
			buildRequireCommand(t, directory, environment, "bash", "-e", "-c", notesCommand)
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := []string{"release", "create", tag, "--verify-tag", "--generate-notes"}
			if strings.Contains(tag, "-") {
				wantArgs = append(wantArgs, "--prerelease")
			}
			if got := strings.Fields(string(args)); !slices.Equal(got, wantArgs) {
				t.Fatalf("release arguments: got %q, want %q", got, wantArgs)
			}
		})
	}
}

func TestReleaseChartUsesPublishedImage(t *testing.T) {
	directory := t.TempDir()
	environment := []string{"GITHUB_REF_NAME=" + releaseTestTag, "RUNNER_TEMP=" + directory}
	buildRequireCommand(t, "../..", environment, "bash", "-e", "-c", releaseStepCommand(t, "chart", "package"))
	archive := filepath.Join(directory, "pxc-anonymizer-"+strings.TrimPrefix(releaseTestTag, "v")+".tgz")
	metadata := buildRequireCommand(t, "../..", nil, "helm", "show", "chart", archive)
	var chart struct {
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal([]byte(metadata), &chart); err != nil {
		t.Fatal(err)
	}
	if chart.Version != strings.TrimPrefix(releaseTestTag, "v") || chart.AppVersion != releaseTestTag {
		t.Fatalf("packaged version=%q appVersion=%q", chart.Version, chart.AppVersion)
	}
	output := buildRequireCommand(t, "../..", nil, "helm", "template", "release-test", archive,
		"--show-only", "templates/deployment.yaml")
	var deployment appsv1.Deployment
	if err := yaml.Unmarshal([]byte(output), &deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("expected one packaged manager container")
	}
	manager := deployment.Spec.Template.Spec.Containers[0]
	wantImage := releaseTestImage + ":" + releaseTestTag
	if manager.Image != wantImage {
		t.Fatalf("packaged manager image=%q, want %q", manager.Image, wantImage)
	}
	var runnerImages []string
	for _, variable := range manager.Env {
		if variable.Name == "PXC_ANONYMIZER_RUNNER_IMAGE" {
			runnerImages = append(runnerImages, variable.Value)
		}
	}
	if !slices.Equal(runnerImages, []string{wantImage}) {
		t.Fatalf("packaged runner images=%q, want %q", runnerImages, wantImage)
	}
}
