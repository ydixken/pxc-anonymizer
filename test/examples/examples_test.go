/*
Copyright 2026 pxc-anonymizer contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package examples_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestExamples(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("example inventory must be nonempty: files=%d error=%v", len(files), err)
	}
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: exampleAssets(t),
		// Ambient USE_EXISTING_CLUSTER must never redirect documentation admission.
		UseExistingCluster: new(bool),
	}
	cfg, err := testEnv.Start()
	t.Cleanup(func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			t.Errorf("stop example API server: %v", stopErr)
		}
	})
	if err != nil {
		t.Fatalf("start example API server: %v", err)
	}
	k8sClient, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatalf("create example API client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespaces := make(map[string]bool)
	kinds := make(map[string]int)
	objects := 0
	for _, file := range files {
		objects += admitExampleFile(t, ctx, k8sClient, file, namespaces, kinds)
	}
	for _, kind := range []string{
		"BackupPointer", "AnonymizationPolicy", "AnonymizationRun", "AnonymizationSchedule", "Bootstrap",
	} {
		if kinds[kind] == 0 {
			t.Errorf("example inventory has no admitted %s", kind)
		}
	}
	if objects == 0 {
		t.Fatal("example inventory admitted no objects")
	}
	if !t.Failed() {
		t.Logf("Admitted every example: %d files, %d objects, all five project kinds.", len(files), objects)
	}
}

func exampleAssets(t *testing.T) string {
	t.Helper()
	if assets := os.Getenv("KUBEBUILDER_ASSETS"); assets != "" {
		return assets
	}
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("locate envtest assets (run make setup-envtest): %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(base, entry.Name())
		}
	}
	t.Fatal("no envtest assets; run make setup-envtest")
	return ""
}

func admitExampleFile(t *testing.T, ctx context.Context, k8sClient client.Client, file string,
	namespaces map[string]bool, kinds map[string]int,
) int {
	t.Helper()
	input, err := os.Open(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			t.Errorf("close %s: %v", file, closeErr)
		}
	}()
	decoder := k8syaml.NewYAMLOrJSONDecoder(input, 4096)
	documents := 0
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(&obj.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode %s document %d: %v", file, documents+1, err)
		}
		if len(obj.Object) == 0 || obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
			t.Fatalf("%s document %d must have apiVersion, kind and metadata.name", file, documents+1)
		}
		namespace := obj.GetNamespace()
		if namespace == "" {
			t.Fatalf("%s document %d %s/%s needs an explicit namespace", file, documents+1, obj.GetKind(), obj.GetName())
		}
		if !namespaces[namespace] {
			if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
				t.Fatalf("prepare namespace for %s document %d: %v", file, documents+1, err)
			}
			namespaces[namespace] = true
		}
		if err := k8sClient.Create(
			ctx, obj, &client.CreateOptions{FieldValidation: metav1.FieldValidationStrict},
		); err != nil {
			t.Fatalf("admit %s document %d %s/%s: %v", file, documents+1, obj.GetKind(), obj.GetName(), err)
		}
		documents++
		kinds[obj.GetKind()]++
		t.Logf("Admitted %s document %d: %s %s/%s", filepath.Base(file), documents, obj.GetKind(), namespace, obj.GetName())
	}
	if documents == 0 {
		t.Fatalf("%s has no YAML documents to admit", file)
	}
	return documents
}
