package buildconfig

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestRunnerImageDefaultsToManagerImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string
		want string
	}{
		{name: "chart defaults"},
		{name: "manager override", set: "image.repository=example.test/manager,image.tag=v2"},
		{
			name: "runner override",
			set: "image.repository=example.test/manager,image.tag=v2," +
				"runner.image.repository=example.test/runner,runner.image.tag=v3",
			want: "example.test/runner:v3",
		},
		{
			name: "runner tag override",
			set:  "image.repository=example.test/manager,image.tag=v2,runner.image.tag=v3",
			want: "example.test/manager:v3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"template", "test", "charts/pxc-anonymizer", "--show-only", "templates/deployment.yaml"}
			if tc.set != "" {
				args = append(args, "--set", tc.set)
			}
			output := buildRequireCommand(t, "../..", nil, "helm", args...)
			var deployment appsv1.Deployment
			if err := yaml.Unmarshal([]byte(output), &deployment); err != nil {
				t.Fatal(err)
			}
			if len(deployment.Spec.Template.Spec.Containers) != 1 {
				t.Fatal("expected exactly one manager container")
			}
			manager := deployment.Spec.Template.Spec.Containers[0]
			want := tc.want
			if want == "" {
				want = manager.Image
			}
			var images []string
			for _, variable := range manager.Env {
				if variable.Name == "PXC_ANONYMIZER_RUNNER_IMAGE" {
					images = append(images, variable.Value)
				}
			}
			for _, arg := range manager.Args {
				if strings.HasPrefix(arg, "--runner-image") {
					t.Fatal("chart must remain compatible with manager images predating the runner flag")
				}
			}
			if len(images) != 1 || images[0] == "" || images[0] != want {
				t.Fatalf("runner images = %v, want exactly %q", images, want)
			}
		})
	}
}
