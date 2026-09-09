// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	seedTestTLSDisabled       = "disabled"
	seedTestTLSConfigDisabled = "false"
)

func TestSeedConfiguration(t *testing.T) {
	passwordPath := filepath.Join(t.TempDir(), "password")
	const password = "synthetic-password\n"
	if err := os.WriteFile(passwordPath, []byte(password), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := seedFlags{}
	flags := seedFlagSet(&settings)
	if err := flags.Parse([]string{"--host=mysql.example.test", "--password-file=" + passwordPath}); err != nil {
		t.Fatal(err)
	}
	configuration, err := prepareSeed(&settings)
	if err != nil {
		t.Fatal(err)
	}
	if settings.scale != 1 || settings.wait != 30*time.Minute || configuration.Addr != "mysql.example.test:3306" ||
		configuration.User != "root" || configuration.Passwd != password || !configuration.MultiStatements ||
		!configuration.ParseTime || configuration.TLSConfig != seedTestTLSConfigDisabled {
		t.Fatal("seed defaults must preserve raw credentials and deployed non-TLS invocation")
	}
	settings.tlsMode = "verify-full"
	configuration, err = prepareSeed(&settings)
	if err != nil || configuration.TLS == nil || configuration.TLS.InsecureSkipVerify ||
		configuration.TLS.ServerName != settings.host {
		t.Fatalf("verified TLS was not configured: %v", err)
	}
	for _, scale := range []int{1, 10} {
		settings.scale = scale
		if _, err := prepareSeed(&settings); err != nil {
			t.Errorf("accepted scale %d: %v", scale, err)
		}
	}
}

func TestSeedValidation(t *testing.T) {
	passwordPath := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordPath, []byte("synthetic-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*seedFlags)
	}{
		{"missing host", func(s *seedFlags) { s.host = "" }},
		{"unsafe host", func(s *seedFlags) { s.host = "host/path" }},
		{"zero port", func(s *seedFlags) { s.port = 0 }},
		{"large port", func(s *seedFlags) { s.port = 65536 }},
		{"missing user", func(s *seedFlags) { s.user = "" }},
		{"zero scale", func(s *seedFlags) { s.scale = 0 }},
		{"large scale", func(s *seedFlags) { s.scale = 11 }},
		{"zero wait", func(s *seedFlags) { s.wait = 0 }},
		{"negative wait", func(s *seedFlags) { s.wait = -time.Second }},
		{"missing password", func(s *seedFlags) { s.passwordFile += ".absent" }},
		{"unknown TLS", func(s *seedFlags) { s.tlsMode = "preferred" }},
		{"CA without TLS", func(s *seedFlags) { s.tlsCAFile = passwordPath }},
		{"invalid CA", func(s *seedFlags) { s.tlsMode, s.tlsCAFile = "verify-full", passwordPath }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := seedFlags{}
			flags := seedFlagSet(&settings)
			if err := flags.Parse([]string{"--host=mysql.example.test", "--password-file=" + passwordPath}); err != nil {
				t.Fatal(err)
			}
			tc.change(&settings)
			if _, err := prepareSeed(&settings); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
	if err := os.WriteFile(passwordPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	settings := seedFlags{host: "mysql.example.test", user: "root", port: 3306, scale: 1,
		wait: time.Second, passwordFile: passwordPath, tlsMode: seedTestTLSDisabled}
	if _, err := prepareSeed(&settings); err == nil {
		t.Fatal("empty password was accepted")
	}
}

func TestSeedInvalidArgumentsAreRedacted(t *testing.T) {
	const sensitive = "do-not-print-this-value"
	for _, args := range [][]string{
		{"--password=" + sensitive}, {"--scale=" + sensitive}, {sensitive},
	} {
		var stdout, stderr bytes.Buffer
		if code := runSeedDemo(t.Context(), args, &stdout, &stderr); code != 1 {
			t.Errorf("invalid arguments exit = %d", code)
		}
		if strings.Contains(stdout.String()+stderr.String(), sensitive) || stderr.Len() == 0 {
			t.Fatal("argument failure must report a redacted explanation")
		}
	}
}

func TestSeedReadinessCancellation(t *testing.T) {
	configuration := mysql.NewConfig()
	configuration.Net = "tcp"
	configuration.Addr = "mysql.example.test:3306"
	connector, err := mysql.NewConnector(configuration)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if _, err := waitSeedConnection(ctx, db, 30*time.Minute); err == nil {
		t.Fatal("canceled readiness wait succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("canceled readiness wait did not stop promptly")
	}
}
