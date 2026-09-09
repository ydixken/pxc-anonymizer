// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

const (
	typeTinyInt       = "tinyint"
	typeSmallInt      = "smallint"
	typeMediumInt     = "mediumint"
	typeInt           = "int"
	typeDateTime      = "datetime"
	typeTimestamp     = "timestamp"
	typeFloat         = "float"
	typeVarchar       = "varchar"
	attributeUnsigned = "unsigned"
	typeDate          = "date"
	typeBigInt        = "bigint"
	typeDecimal       = "decimal"
	typeDouble        = "double"
)

func validateValue(value any, column columnInfo) error {
	if value == nil {
		if !column.nullable {
			return report.Fail(report.Schema, "replacement is null for a non-null column")
		}
		return nil
	}
	if column.length.Valid {
		text := fmt.Sprint(value)
		length := utf8.RuneCountInString(text)
		if strings.Contains(column.kind, "binary") || strings.Contains(column.kind, "blob") {
			length = len(text)
		}
		if int64(length) > column.length.Int64 {
			return report.Fail(report.Schema, "replacement exceeds column capacity")
		}
	}
	if err := numericValue(fmt.Sprint(value), column); err != nil {
		return err
	}
	if column.kind == typeDate || column.kind == typeDateTime || column.kind == typeTimestamp {
		_, dateErr := time.Parse(time.DateOnly, fmt.Sprint(value))
		_, datetimeErr := time.Parse(time.DateTime, fmt.Sprint(value))
		if dateErr != nil && (column.kind == typeDate || datetimeErr != nil) {
			return report.Fail(report.Schema, "replacement does not match the temporal column type")
		}
	}
	return nil
}

var decimalPattern = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

func numericValue(value string, column columnInfo) error {
	bits := map[string]uint{typeTinyInt: 8, typeSmallInt: 16, typeMediumInt: 24, typeInt: 32, typeBigInt: 64}[column.kind]
	if bits > 0 {
		integer, valid := new(big.Int).SetString(value, 10)
		if len(value) > 21 || !valid {
			return report.Fail(report.Schema, "replacement is not an integer")
		}
		maximum := new(big.Int).Lsh(big.NewInt(1), bits)
		minimum := new(big.Int)
		if !strings.Contains(column.fullType, attributeUnsigned) {
			maximum.Rsh(maximum, 1)
			minimum.Neg(new(big.Int).Set(maximum))
		}
		maximum.Sub(maximum, big.NewInt(1))
		if integer.Cmp(minimum) < 0 || integer.Cmp(maximum) > 0 {
			return report.Fail(report.Schema, "replacement exceeds integer column range")
		}
	}
	if column.kind == typeDecimal {
		if len(value) > 128 || !decimalPattern.MatchString(value) {
			return report.Fail(report.Schema, "replacement is not a bounded decimal")
		}
		var precision, scale int
		if _, err := fmt.Sscanf(column.fullType, "decimal(%d,%d)", &precision, &scale); err != nil {
			return report.Fail(report.Schema, "decimal column definition is unsupported")
		}
		parts := strings.Split(strings.TrimLeft(value, "+-"), ".")
		integerDigits := len(strings.TrimLeft(parts[0], "0"))
		if integerDigits > precision-scale || (len(parts) == 2 && len(parts[1]) > scale) || (strings.Contains(column.fullType, attributeUnsigned) && strings.HasPrefix(value, "-")) {
			return report.Fail(report.Schema, "replacement exceeds decimal precision or scale")
		}
	}
	if column.kind == typeFloat || column.kind == typeDouble {
		bitSize := 64
		if column.kind == typeFloat {
			bitSize = 32
		}
		number, err := strconv.ParseFloat(value, bitSize)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || (strings.Contains(column.fullType, attributeUnsigned) && number < 0) {
			return report.Fail(report.Schema, "replacement exceeds floating-point column range")
		}
	}
	return nil
}
