package buildconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	demoPatchTestOperation = "test"
	demoCreateVerb         = "create"
	demoRunFixture         = "run.json"
	demoBootstrapFixture   = "bootstrap.json"
	demoSeedScript         = "01-seed-db.sh"
	demoApplyScript        = "02-apply-manifests.sh"
	demoWatchScript        = "03-watch-resources.sh"
)

const demoKubectlStub = `#!/bin/bash
printf '%s\0' "$@" >> "$DEMO_SCRIPT_CALLS"
printf '\0' >> "$DEMO_SCRIPT_CALLS"
if [[ $5 == create ]]; then
  /bin/cat > "$DEMO_SCRIPT_BODY"
fi
[[ ${DEMO_SCRIPT_FAIL:-} != "$5" ]] || exit 7
if [[ $5 == create && ${DEMO_SCRIPT_EMPTY:-} != true ]]; then
  printf 'job.batch/demo-seed-abc12\n'
fi
`

func demoScriptsFixture(t *testing.T, withKubectl bool) string {
	t.Helper()
	dir := t.TempDir()
	if withKubectl {
		if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(demoKubectlStub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func demoScriptsRun(
	t *testing.T, dir, script string, environment []string, args ...string,
) (string, [][]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	scriptPath := filepath.Join("..", "..", "scripts", demoFixtureName, script)
	command := exec.CommandContext(ctx, "/bin/bash", append([]string{scriptPath}, args...)...)
	command.Env = append([]string{
		"PATH=" + dir, "DEMO_SCRIPT_CALLS=" + filepath.Join(dir, "calls"),
		"DEMO_SCRIPT_BODY=" + filepath.Join(dir, "job.json"),
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

func demoSeedArgs() []string {
	return []string{
		demoContextFlag, "demo-context", demoNamespaceFlag, demoSourceNamespace,
		"--host", "source.source-demo.svc.cluster.local", "--secret", "source-users", "--image", "example.com/demo:v1",
	}
}

func TestDemoSeedJobUsesExistingCommandAndMountedCredentials(t *testing.T) {
	dir := demoScriptsFixture(t, true)
	args := demoSeedArgs()
	args[1] = "demo 'context' $(printf bad > " + filepath.Join(dir, "injected") + ")"
	output, calls, err := demoScriptsRun(t, dir, demoSeedScript, nil, args...)
	if err != nil || len(calls) != 2 {
		t.Fatalf("seed: error=%v output=%q calls=%v", err, output, calls)
	}
	for _, call := range calls {
		if !slices.Equal(call[:4], []string{demoContextFlag, args[1], demoNamespaceFlag, demoSourceNamespace}) {
			t.Fatalf("lost explicit context/namespace: %q", call)
		}
	}
	if calls[0][4] != demoCreateVerb || !slices.Contains(calls[0], "-") || calls[1][4] != "wait" ||
		!slices.Contains(calls[1], "job.batch/demo-seed-abc12") ||
		!slices.Contains(calls[1], "--for=condition=Complete") || !slices.Contains(calls[1], "--timeout=36m") {
		t.Fatalf("want one fresh creation and bounded completion wait, got %v", calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	metadata := job["metadata"].(map[string]any)
	spec := job["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	security := pod["securityContext"].(map[string]any)
	containerSecurity := container["securityContext"].(map[string]any)
	volume := pod["volumes"].([]any)[0].(map[string]any)["secret"].(map[string]any)
	checks := []struct {
		name      string
		got, want any
	}{
		{"kind", job["kind"], "Job"},
		{"fresh name", metadata["generateName"], "demo-seed-"},
		{"no reused name", metadata["name"], nil},
		{"namespace", metadata["namespace"], demoSourceNamespace},
		{"no existing owner", metadata["ownerReferences"], nil},
		{"no retry", spec["backoffLimit"], float64(0)},
		{"job deadline", spec["activeDeadlineSeconds"], float64(2100)},
		{"own job TTL", spec["ttlSecondsAfterFinished"], float64(3600)},
		{"no container retry", pod["restartPolicy"], "Never"},
		{"no API token", pod["automountServiceAccountToken"], false},
		{"nonroot", security["runAsNonRoot"], true},
		{"UID", security["runAsUser"], float64(65532)},
		{"Secret file group", security["fsGroup"], float64(65532)},
		{"readonly filesystem", containerSecurity["readOnlyRootFilesystem"], true},
		{"no privilege escalation", containerSecurity["allowPrivilegeEscalation"], false},
		{"image ENTRYPOINT", container["command"], nil},
		{"requested image", container["image"], "example.com/demo:v1"},
		{"no password environment", container["env"], nil},
		{"root Secret reference", volume["secretName"], "source-users"},
		{"restrictive Secret mode", volume["defaultMode"], float64(256)},
		{"only root key", volume["items"], []any{map[string]any{"key": "root", "path": "root"}}},
		{"readonly password mount", container["volumeMounts"], []any{map[string]any{
			"name": "pxc-users", "mountPath": "/var/run/secrets/pxc", "readOnly": true,
		}}},
		{"existing seed command", container["args"], []any{
			"seed-demo", "--host=source.source-demo.svc.cluster.local", "--port=3306", "--user=root",
			"--password-file=/var/run/secrets/pxc/root", "--wait=30m", "--scale=1",
		}},
	}
	for _, check := range checks {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s: got %v, want %v", check.name, check.got, check.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatalf("context executed as shell code: %v", err)
	}
}

func TestDemoScriptsRejectInvalidInputsBeforeKubectl(t *testing.T) {
	for _, tt := range []struct {
		name   string
		script string
		args   []string
	}{
		{name: "seed missing input", script: demoSeedScript},
		{name: "seed JSON injection", script: demoSeedScript,
			args: append(demoSeedArgs(), "--host", `host", "other":"value`)},
		{name: "seed image injection", script: demoSeedScript,
			args: append(demoSeedArgs(), "--image", "image\nother")},
		{name: "seed scale out of range", script: demoSeedScript,
			args: append(demoSeedArgs(), "--scale", "11")},
		{name: "seed invalid namespace", script: demoSeedScript,
			args: append(demoSeedArgs(), demoNamespaceFlag, "Bad")},
		{name: "apply no files", script: demoApplyScript,
			args: []string{demoContextFlag, demoFixtureName, demoNamespaceFlag, demoFixtureName}},
		{name: "apply missing file", script: demoApplyScript,
			args: []string{demoContextFlag, demoFixtureName, demoNamespaceFlag, demoFixtureName, "does-not-exist.yaml"}},
		{name: "watch no resource", script: demoWatchScript,
			args: []string{demoContextFlag, demoFixtureName, demoNamespaceFlag, demoFixtureName}},
		{name: "watch multiple kinds", script: demoWatchScript,
			args: []string{demoContextFlag, demoFixtureName, demoNamespaceFlag, demoFixtureName, "pods,jobs"}},
		{name: "watch option injection", script: demoWatchScript,
			args: []string{demoContextFlag, demoFixtureName, demoNamespaceFlag, demoFixtureName, "pods", "--all-namespaces"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := demoScriptsFixture(t, true)
			output, calls, err := demoScriptsRun(t, dir, tt.script, nil, tt.args...)
			if err == nil || len(calls) != 0 {
				t.Fatalf("input accepted or contacted kubectl: error=%v output=%q calls=%v", err, output, calls)
			}
		})
	}
}

func TestDemoWatchPreservesNativeArgumentsAndErrors(t *testing.T) {
	dir := demoScriptsFixture(t, true)
	args := []string{demoContextFlag, "demo 'quoted' context", demoNamespaceFlag, demoFixtureName,
		"anonymizationruns", "run-one"}
	output, calls, err := demoScriptsRun(t, dir, demoWatchScript, []string{"DEMO_SCRIPT_FAIL=get"}, args...)
	want := append(args[:4:4], "get", "anonymizationruns", "run-one", "--watch")
	if err == nil || len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("native exit/arguments changed: error=%v output=%q calls=%v", err, output, calls)
	}
}

func TestDemoSeedFailureDoesNotRetryOrClaimSuccess(t *testing.T) {
	for _, tt := range []struct {
		name  string
		env   string
		calls int
	}{
		{name: "create rejected", env: "DEMO_SCRIPT_FAIL=create", calls: 1},
		{name: "missing created identity", env: "DEMO_SCRIPT_EMPTY=true", calls: 1},
		{name: "completion unconfirmed", env: "DEMO_SCRIPT_FAIL=wait", calls: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := demoScriptsFixture(t, true)
			output, calls, err := demoScriptsRun(t, dir, demoSeedScript, []string{tt.env}, demoSeedArgs()...)
			if err == nil || len(calls) != tt.calls {
				t.Fatalf("seed failure was ignored/retried: error=%v output=%q calls=%v", err, output, calls)
			}
		})
	}
}

func TestDemoScriptsRequireKubectl(t *testing.T) {
	for _, script := range []string{demoSeedScript, demoApplyScript, demoWatchScript} {
		t.Run(script, func(t *testing.T) {
			dir := demoScriptsFixture(t, false)
			args := demoSeedArgs()
			switch script {
			case demoApplyScript:
				file := filepath.Join(dir, "manifest.yaml")
				if err := os.WriteFile(file, []byte("kind: ConfigMap\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = demoPipelineArgs(file, file)
			case demoWatchScript:
				args = append(args[:4:4], "pods")
			}
			output, calls, err := demoScriptsRun(t, dir, script, nil, args...)
			if err == nil || len(calls) != 0 || !strings.Contains(output, "kubectl is required") {
				t.Fatalf("missing kubectl not reported: error=%v output=%q", err, output)
			}
		})
	}
}

const demoPipelineKubectl = `#!/bin/bash
set -eu
printf '%s\0' "$@" >> "$DEMO_SCRIPT_CALLS"
printf '\0' >> "$DEMO_SCRIPT_CALLS"
verb=$5 resource=${6:-} name=${7:-}
fixture=$DEMO_PIPELINE_FIXTURES
if [[ $verb == annotate ]]; then
  [[ ${DEMO_PIPELINE_CASE:-} != annotation-conflict ]] || exit 7
  : > "$fixture/triggered"
  exit 0
fi
if [[ $verb == create || $verb == patch ]]; then
  if [[ $verb == create ]]; then
    /bin/cat > "$fixture/created-body.json"
  fi
  if [[ -f $fixture/triggered ]]; then
    : > "$fixture/restoring"
    /bin/cat "$fixture/bootstrap.json"
  else
    /bin/cat "$fixture/backup.json"
  fi
  exit 0
fi
[[ $verb == get ]] || exit 8
case "$resource" in
  anonymizationruns.*)
    if [[ $name != -o ]]; then file=run.json
    elif [[ -f $fixture/triggered ]]; then file=runs-issued.json
    else file=runs-idle.json; fi ;;
  anonymizationschedules.*) file=schedule.json ;;
  backuppointers.*) file=pointer.json ;;
  perconaxtradbclusterbackups.*)
    if [[ $name == output-backup ]]; then file=output-backup.json; else file=backup.json; fi ;;
  perconaxtradbclusters.*) file=target.json ;;
  bootstraps.*)
    if [[ $name == -o ]]; then file=bootstraps-idle.json
    elif [[ -f $fixture/restoring ]]; then file=bootstrap.json
    else file=bootstrap-existing.json; fi ;;
  databases.*) file=crossplane.json ;;
  *) exit 9 ;;
esac
/bin/cat "$fixture/$file"
`

func demoPipelineArgs(backup, bootstrap string) []string {
	return []string{demoContextFlag, "quoted 'context' $(false)",
		"--source-namespace", demoSourceNamespace, "--operator-namespace", demoPipelineNamespace,
		"--downstream-namespace", strings.Join([]string{"downstream", demoFixtureName}, "-"), "--source-backup", backup,
		"--source-pointer", "source-pointer", "--schedule", "demo-schedule", "--bootstrap", bootstrap,
		"--timeout-seconds", "1"}
}

func demoPipelineFixture(t *testing.T) (string, []string) {
	t.Helper()
	dir := demoScriptsFixture(t, false)
	for _, tool := range []string{"jq", "shasum", "mktemp", "sleep"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("kubectl", demoPipelineKubectl)
	if err := os.Chmod(filepath.Join(dir, "kubectl"), 0o700); err != nil {
		t.Fatal(err)
	}
	write("source-template.json", `{"apiVersion":"pxc.percona.com/v1","kind":"PerconaXtraDBClusterBackup",
 "metadata":{"generateName":"source-","namespace":"source-demo"},
 "spec":{"pxcCluster":"source","storageName":"daily"}}`)
	write("bootstrap-template.json", `{"apiVersion":"pxc-anonymizer.io/v1alpha1","kind":"Bootstrap",
 "metadata":{"name":"talk-demo","namespace":"downstream-demo",
 "annotations":{"pxc-anonymizer.io/demo-crossplane-count":"1"}},
 "spec":{"pointer":{"http":{"url":"https://example.com/output"}},
 "restore":{"credentialsSecret":"restore-credentials"},"targets":[{"pxcCluster":"downstream"}],
 "crossplane":{"group":"mysql.sql.crossplane.io","kinds":["databases","users","grants"],
 "pause":true,"resume":true,"selector":{"matchLabels":{"app":"demo"}}}}}`)
	write("backup.json", `{"metadata":{"name":"source-backup","uid":"source-uid",
 "creationTimestamp":"2026-01-01T00:00:00Z"},"spec":{"pxcCluster":"source","storageName":"daily"},
 "status":{"state":"Succeeded","destination":"s3://example/source","completed":"2026-01-01T00:00:01Z"}}`)
	write("pointer.json", `{"metadata":{"uid":"pointer-uid","generation":1},
 "spec":{"source":{"pxcCluster":"source","storageNames":["daily"]}},
 "status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":1},
 {"type":"Fresh","status":"True","observedGeneration":1}],"current":{"backupName":"source-backup",
 "destination":"s3://example/source","completedAt":"2026-01-01T00:00:01Z",
 "publishedAt":"2026-01-01T00:00:02Z","schemaVersion":2}}}`)
	write("schedule.json", `{"metadata":{"name":"demo-schedule","uid":"schedule-uid","generation":1,
 "resourceVersion":"42"},"spec":{"suspend":false,"concurrencyPolicy":"Forbid",
 "template":{"spec":{"output":{"pointer":{"publicURL":"https://example.com/output"}}}}},
 "status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}`)
	write("target.json", `{"metadata":{"uid":"target-uid"},"status":{"state":"ready"}}`)
	write("crossplane.json", `{"items":[{"kind":"Database","metadata":{"name":"demo-db","uid":"db-uid"},
 "status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Synced","status":"True"}]}}]}`)
	write("runs-idle.json", `{"items":[]}`)
	write("bootstraps-idle.json", `{"items":[]}`)
	write("bootstrap-existing.json", "")
	write(demoRunFixture, `{"metadata":{"name":"owned-run","namespace":"pipeline-demo","uid":"run-uid","generation":1,
 "labels":{"pxc-anonymizer.io/schedule":"demo-schedule"},
 "annotations":{"pxc-anonymizer.io/run-now-token-hash":"HASH"},
 "ownerReferences":[{"apiVersion":"pxc-anonymizer.io/v1alpha1","kind":"AnonymizationSchedule",
 "name":"demo-schedule","uid":"schedule-uid","controller":true}]},
 "status":{"phase":"Completed","observedGeneration":1,"tempCluster":{"name":"temporary"},
 "source":{"backupName":"source-backup","destination":"s3://example/source","sourceCluster":"source",
 "pointerPublishedAt":"2026-01-01T00:00:02Z","pointerSchemaVersion":2},
 "output":{"backupName":"output-backup","destination":"s3://example/output","storageName":"output",
 "completedAt":"2026-01-01T00:00:03Z","publishedAt":"2026-01-01T00:00:04Z","etag":"etag","schemaVersion":2},
 "conditions":[{"type":"Complete","status":"True","observedGeneration":1},
 {"type":"Anonymized","status":"True","observedGeneration":1},
 {"type":"BackedUp","status":"True","observedGeneration":1},
 {"type":"Published","status":"True","observedGeneration":1}]}}`)
	data, err := os.ReadFile(filepath.Join(dir, demoRunFixture))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("source-uid"))
	run := strings.ReplaceAll(string(data), "HASH", hex.EncodeToString(hash[:]))
	write(demoRunFixture, run)
	write("runs-issued.json", `{"items":[`+run+`]}`)
	write("output-backup.json", `{"metadata":{"name":"output-backup","uid":"output-uid"},
 "spec":{"pxcCluster":"temporary","storageName":"output"},
 "status":{"state":"Succeeded","destination":"s3://example/output","completed":"2026-01-01T00:00:03Z"}}`)
	write(demoBootstrapFixture, `{"metadata":{"name":"talk-demo","uid":"bootstrap-uid","generation":1},
 "spec":{"trigger":"run-uid"},"status":{"phase":"Completed","observedGeneration":1,"observedTrigger":"run-uid",
 "source":{"backupName":"output-backup","destination":"s3://example/output","sourceCluster":"temporary",
 "pointerPublishedAt":"2026-01-01T00:00:04Z","pointerSchemaVersion":2},
 "conditions":[{"type":"Complete","status":"True","observedGeneration":1}],
 "execution":{"id":"execution-1","targets":[{"uid":"target-uid","restoreUID":"restore-uid"}],
 "selected":[{"kind":"databases","name":"demo-db","uid":"db-uid"}]},
 "crossplane":{"paused":[{"kind":"databases","name":"demo-db","uid":"db-uid"}],
 "resumed":[{"kind":"databases","name":"demo-db","uid":"db-uid"}]}}}`)
	return dir, demoPipelineArgs(filepath.Join(dir, "source-template.json"), filepath.Join(dir, "bootstrap-template.json"))
}

func TestDemoPipelineBindsFreshBackupScheduleAndBootstrap(t *testing.T) {
	dir, args := demoPipelineFixture(t)
	output, calls, err := demoScriptsRun(t, dir, demoApplyScript,
		[]string{"DEMO_PIPELINE_FIXTURES=" + dir, "TMPDIR=" + dir}, args...)
	if err != nil {
		t.Fatalf("pipeline failed: %v\n%s", err, output)
	}
	t.Logf("Simulated pipeline output (inert kubectl fixtures):\n%s", output)
	var mutations [][]string
	for _, call := range calls {
		if call[0] != demoContextFlag || call[1] != args[1] || call[2] != demoNamespaceFlag {
			t.Fatalf("explicit quoted context/namespace lost: %q", call)
		}
		if call[4] != "get" {
			mutations = append(mutations, call)
		}
	}
	if len(mutations) != 3 || mutations[0][4] != demoCreateVerb || mutations[1][4] != "annotate" ||
		mutations[2][4] != demoCreateVerb || !slices.Contains(mutations[1], "pxc-anonymizer.io/run-now=source-uid") ||
		!slices.Contains(mutations[1], "--resource-version") {
		t.Fatalf("expected only backup creation, guarded Schedule annotation and Bootstrap creation: %v", mutations)
	}
	data, err := os.ReadFile(filepath.Join(dir, "created-body.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap struct {
		Spec struct {
			Trigger string `json:"trigger"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap.Spec.Trigger != "run-uid" || !strings.Contains(output, "Controller lifecycle complete") {
		t.Fatalf("missing Run-bound completion: body=%s output=%q", data, output)
	}
}

func TestDemoPipelineRejectsUnsafeOrUnprovenTransitions(t *testing.T) {
	for _, tt := range []struct {
		name, file, old, replacement string
		mutations                    int
	}{
		{"human pause", "crossplane.json", `"name":"demo-db"`,
			`"annotations":{"crossplane.io/paused":"true"},"name":"demo-db"`, 0},
		{"foreign pause owner", "crossplane.json", `"name":"demo-db"`,
			`"annotations":{"pxc-anonymizer.io/bootstrap-pause-owner":"foreign"},"name":"demo-db"`, 0},
		{"active Run", "runs-idle.json", `[]`, `[{"status":{"phase":"Restoring"}}]`, 0},
		{"foreign Bootstrap", "bootstrap-existing.json", "", `{}`, 0},
		{"annotation-conflict", "schedule.json", `"42"`, `"42"`, 2},
		{"foreign Run owner", "runs-issued.json", `"uid":"schedule-uid"`, `"uid":"foreign"`, 2},
		{"wrong source", demoRunFixture, `"sourceCluster":"source"`, `"sourceCluster":"wrong"`, 2},
		{"failed Run", demoRunFixture, `"phase":"Completed"`, `"phase":"Failed"`, 2},
		{"missing output", demoRunFixture, `"etag":"etag"`, `"etag":""`, 2},
		{"wrong output backup", "output-backup.json", `"destination":"s3://example/output"`,
			`"destination":"s3://example/other"`, 2},
		{"wrong downstream source", demoBootstrapFixture, `"sourceCluster":"temporary"`,
			`"sourceCluster":"wrong"`, 3},
		{"missing resume proof", demoBootstrapFixture, `"resumed":[{"kind":"databases","name":"demo-db","uid":"db-uid"}]`,
			`"resumed":[]`, 3},
		{"stale completion", demoBootstrapFixture, `"observedGeneration":1`, `"observedGeneration":0`, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, args := demoPipelineFixture(t)
			path := filepath.Join(dir, tt.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tt.old) {
				t.Fatal("fixture replacement did not match")
			}
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), tt.old, tt.replacement)), 0o600); err != nil {
				t.Fatal(err)
			}
			output, calls, err := demoScriptsRun(t, dir, demoApplyScript,
				[]string{"DEMO_PIPELINE_FIXTURES=" + dir, "TMPDIR=" + dir, "DEMO_PIPELINE_CASE=" + tt.name}, args...)
			mutations := 0
			for _, call := range calls {
				if call[4] != "get" {
					mutations++
				}
			}
			if err == nil || strings.Contains(output, "Controller lifecycle complete") || mutations != tt.mutations {
				t.Fatalf("unsafe transition: err=%v mutations=%d want=%d\n%s", err, mutations, tt.mutations, output)
			}
		})
	}
}

func TestDemoPipelineRearmsOnlyTheRecordedOwnedBootstrap(t *testing.T) {
	dir, args := demoPipelineFixture(t)
	template, err := os.ReadFile(filepath.Join(dir, "bootstrap-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var existing map[string]any
	if err := json.Unmarshal(template, &existing); err != nil {
		t.Fatal(err)
	}
	metadata := existing["metadata"].(map[string]any)
	metadata["uid"] = "bootstrap-uid"
	metadata["resourceVersion"] = "53"
	metadata["generation"] = 1
	metadata["labels"] = map[string]string{"app.kubernetes.io/managed-by": "pxc-anonymizer-demo-pipeline"}
	existing["spec"].(map[string]any)["trigger"] = "prior-run"
	existing["status"] = map[string]any{
		"phase": "Completed", "observedGeneration": 1, "observedTrigger": "prior-run",
		"execution":  map[string]string{"id": "prior-execution"},
		"conditions": []map[string]any{{"type": "Complete", "status": "True", "observedGeneration": 1}},
	}
	data, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bootstrap-existing.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	output, calls, err := demoScriptsRun(t, dir, demoApplyScript,
		[]string{"DEMO_PIPELINE_FIXTURES=" + dir, "TMPDIR=" + dir}, args...)
	if err != nil {
		t.Fatalf("owned rearm failed: %v\n%s", err, output)
	}
	var patches []string
	for _, call := range calls {
		if call[4] == "patch" {
			index := slices.Index(call, "-p")
			if index < 0 || index+1 >= len(call) {
				t.Fatalf("missing JSON patch: %v", call)
			}
			patches = append(patches, call[index+1])
		}
	}
	if len(patches) != 1 {
		t.Fatalf("expected exactly one rearm mutation, got %v", patches)
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	var patch []operation
	if err := json.Unmarshal([]byte(patches[0]), &patch); err != nil {
		t.Fatal(err)
	}
	want := []operation{
		{demoPatchTestOperation, "/metadata/uid", "bootstrap-uid"},
		{demoPatchTestOperation, "/metadata/resourceVersion", "53"},
		{"add", "/spec/trigger", "run-uid"},
	}
	if !reflect.DeepEqual(patch, want) {
		t.Fatalf("lost identity/version guards: %v", patch)
	}
}
