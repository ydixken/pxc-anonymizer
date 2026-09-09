// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/ydixken/pxc-anonymizer/internal/runner/sqlstep"
)

func TestErrorClassesAndNoSensitiveDetails(t *testing.T) {
	const sensitive = "private-row-value credential=example SQL body example"
	for number, expected := range map[uint16]struct {
		class Class
		exit  int
	}{1044: {Permission, 12}, 1045: {Permission, 12}, 1142: {Permission, 12}, 1227: {Permission, 12},
		1146: {Schema, 11}, 1054: {Schema, 11}, 1062: {Unique, 13}, 1213: {Transient, 1}, 1205: {Transient, 1}, 2013: {Transient, 1}} {
		class, message := Classify(&mysql.MySQLError{Number: number, Message: sensitive})
		result := Report{Result: ResultError, Class: class, Message: message}
		var output bytes.Buffer
		if err := result.Write(&output); err != nil || class != expected.class || result.ExitCode() != expected.exit {
			t.Fatalf("MySQL %d classification failed", number)
		}
		if strings.Contains(output.String(), sensitive) || strings.Contains(output.String(), "credential") {
			t.Fatal("database error payload leaked")
		}
	}
	for _, err := range []error{sqlstep.ErrUncertainOutcome, errors.Join(sqlstep.ErrUncertainOutcome, &mysql.MySQLError{Number: 1064, Message: sensitive})} {
		class, message := Classify(err)
		if class != Policy || strings.Contains(message, sensitive) {
			t.Fatal("uncertain SQL outcome was not a safe non-retryable policy failure")
		}
	}
	_, message := Classify(errors.New(sensitive))
	if strings.Contains(message, sensitive) {
		t.Fatal("generic I/O detail leaked")
	}
}

func TestTerminationBudgetAndExitContract(t *testing.T) {
	var output bytes.Buffer
	result := Report{Result: ResultError, Class: Policy, Message: strings.Repeat("界", 5000), PolicyHash: "sha256:" + strings.Repeat("a", 64)}
	if err := result.Write(&output); err != nil {
		t.Fatal(err)
	}
	if output.Len() > 4096 || !json.Valid(bytes.TrimSpace(output.Bytes())) || result.ExitCode() != 10 {
		t.Fatal("termination contract was not bounded valid JSON")
	}
	if (Report{Result: ResultOK}).ExitCode() != 0 || (Report{Result: ResultError}).ExitCode() != 1 {
		t.Fatal("success or default failure exit code changed")
	}
}
