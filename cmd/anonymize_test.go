// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

func TestAnonymizeRawFilesAndVerifiedTLS(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"policy": []byte(`{"databases":[]}`), "seed": bytes.Repeat([]byte{'A'}, 32), "password": []byte("test-value\n"),
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), value, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	settings := anonymizeFlags{}
	flags := anonymizeFlagSet(&settings)
	if err := flags.Parse([]string{
		"--host=database.example", "--reference-time=2026-01-02T00:00:00Z",
		"--policy=" + filepath.Join(root, "policy"), "--seed-file=" + filepath.Join(root, "seed"),
		"--password-file=" + filepath.Join(root, "password"),
	}); err != nil {
		t.Fatal(err)
	}
	configuration, err := prepareAnonymize(&settings)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Passwd != string(files["password"]) || !bytes.Equal(settings.options.Seed, files["seed"]) {
		t.Fatal("raw password or seed bytes were transformed")
	}
	if configuration.TLS == nil || configuration.TLS.InsecureSkipVerify || configuration.AllowFallbackToPlaintext ||
		configuration.TLS.ServerName != "database.example" || !configuration.MultiStatements {
		t.Fatal("verified TLS or server-side SQL splitting was not configured")
	}
	settings.tlsMode = "disabled"
	configuration, err = prepareAnonymize(&settings)
	if err != nil || configuration.TLS != nil || configuration.TLSConfig != "false" {
		t.Fatal("explicit temporary-cluster plaintext selection failed")
	}
	for _, workers := range []int{0, 1, 32, 33} {
		settings.options.Workers = workers
		_, err := prepareAnonymize(&settings)
		valid := workers >= 1 && workers <= 32
		if (err == nil) != valid {
			t.Fatalf("worker boundary %d: valid=%t error=%v", workers, valid, err)
		}
	}
}

func TestAnonymizeInvalidFlagsAreRedacted(t *testing.T) {
	const sensitive = "must-not-appear-in-output"
	path := filepath.Join(t.TempDir(), "termination.json")
	var stdout, stderr bytes.Buffer
	args := []string{"--termination-file=" + path, "--unsupported=" + sensitive}
	exit := runAnonymize(context.Background(), args, &stdout, &stderr)
	if exit != 10 || strings.Contains(stdout.String()+stderr.String(), sensitive) {
		t.Fatal("invalid flags leaked values or returned the wrong exit code")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result report.Report
	if err := json.Unmarshal(data, &result); err != nil || result.Class != report.Policy {
		t.Fatal("policy failure was not written to termination JSON")
	}
}
