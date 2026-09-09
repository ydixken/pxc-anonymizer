// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/ydixken/pxc-anonymizer/internal/runner/sqlstep"
)

const ResultOK = "ok"
const ResultError = "error"

type Class string

const (
	Transient  Class = "transient"
	Policy     Class = "policy"
	Schema     Class = "schema"
	Permission Class = "permission"
	Unique     Class = "unique"
)

type Report struct {
	Result          string  `json:"result"`
	Class           Class   `json:"class,omitempty"`
	Message         string  `json:"message"`
	TablesDone      int     `json:"tablesDone"`
	TablesTotal     int     `json:"tablesTotal"`
	RowsDone        int64   `json:"rowsDone"`
	StepsDone       int     `json:"stepsDone"`
	DurationSeconds float64 `json:"durationSeconds"`
	PolicyHash      string  `json:"policyHash"`
}

type Failure struct {
	Class   Class
	Message string
}

func (e *Failure) Error() string { return e.Message }

func Fail(class Class, safeMessage string) error {
	return &Failure{Class: class, Message: safeMessage}
}

// Database errors can contain row values, SQL and credentials; only their numeric code is public.
func Classify(err error) (Class, string) {
	if failure, ok := errors.AsType[*Failure](err); ok {
		return failure.Class, failure.Message
	}
	if databaseError, ok := errors.AsType[*mysql.MySQLError](err); ok {
		class := Transient
		switch databaseError.Number {
		case 1044, 1045, 1142, 1227:
			class = Permission
		case 1146, 1054:
			class = Schema
		case 1062:
			class = Unique
		}
		if class == Transient && errors.Is(err, sqlstep.ErrUncertainOutcome) {
			class = Policy
		}
		return class, fmt.Sprintf("database operation failed (MySQL %d)", databaseError.Number)
	}
	if errors.Is(err, sqlstep.ErrUncertainOutcome) {
		return Policy, "SQL step outcome is uncertain; explicit recovery is required"
	}
	return Transient, "database or I/O operation failed"
}

func (r Report) ExitCode() int {
	if r.Result == ResultOK {
		return 0
	}
	switch r.Class {
	case Policy:
		return 10
	case Schema:
		return 11
	case Permission:
		return 12
	case Unique:
		return 13
	default:
		return 1
	}
}

func (r Report) Write(writer io.Writer) error {
	if !utf8.ValidString(r.Message) {
		r.Message = "operation failed"
	}
	message := []rune(r.Message)
	r.Message = string(message[:min(len(message), 512)])
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data)+1 > 4096 {
		return errors.New("termination report exceeds 4 KiB")
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}
