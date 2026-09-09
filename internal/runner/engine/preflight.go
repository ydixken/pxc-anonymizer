// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/runner/pager"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
	"github.com/ydixken/pxc-anonymizer/internal/runner/strategy"
)

type tablePlan struct {
	page     pager.Table
	action   api.TableAction
	columns  []*strategy.Column
	metadata []columnInfo
}

type databasePlan struct {
	name   string
	policy api.DatabasePolicy
	tables []tablePlan
}

type executionPlan struct {
	databases []databasePlan
	steps     map[string]string
	total     int
}

type columnInfo struct {
	name, kind, fullType string
	length               sql.NullInt64
	nullable             bool
	generated            bool
}

type indexInfo struct {
	names  []string
	unique bool
	full   bool
}

func preflight(ctx context.Context, db *sql.DB, spec api.AnonymizationPolicySpec, options Options) (executionPlan, error) {
	plan := executionPlan{steps: make(map[string]string, len(spec.Steps))}
	for _, step := range spec.Steps {
		path, err := projectedPath(options.StepsDir, step.Name+".sql")
		if err != nil {
			return plan, report.Fail(report.Policy, "invalid SQL step file path")
		}
		data, err := ReadFile(path, 1<<20)
		if err != nil || len(data) == 0 {
			return plan, report.Fail(report.Policy, "SQL step file is missing, empty or too large")
		}
		plan.steps[step.Name] = string(data)
	}
	rows, err := db.QueryContext(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
	if err != nil {
		return plan, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return plan, err
		}
		names = append(names, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return plan, err
	}
	seen := map[string]bool{}
	for _, rule := range spec.Databases {
		matched := []string{}
		for _, name := range names {
			if matchesDatabase(rule, name) {
				matched = append(matched, name)
			}
		}
		if len(matched) == 0 && !rule.Optional {
			return plan, report.Fail(report.Schema, "required database or database pattern has no match")
		}
		for _, name := range matched {
			if seen[name] || name == "_pxc_anonymizer" {
				return plan, report.Fail(report.Policy, "overlapping or reserved database policy")
			}
			seen[name] = true
			item, err := planDatabase(ctx, db, name, rule, spec.Defaults, options)
			if err != nil {
				return plan, err
			}
			plan.databases = append(plan.databases, item)
			plan.total += len(item.tables)
		}
	}
	return plan, nil
}

func matchesDatabase(rule api.DatabasePolicy, name string) bool {
	if rule.Name != "" {
		return rule.Name == name
	}
	pattern, err := regexp.Compile("^(?:" + rule.NamePattern + ")$")
	return err == nil && pattern.MatchString(name)
}

func planDatabase(ctx context.Context, db *sql.DB, name string, rule api.DatabasePolicy, defaults *api.PolicyDefaults, options Options) (databasePlan, error) {
	plan := databasePlan{name: name, policy: rule}
	if _, err := pager.QuoteIdentifier(name); err != nil {
		return plan, report.Fail(report.Schema, "database name is not a supported SQL identifier")
	}
	for _, table := range rule.Tables {
		var kind string
		var storageEngine sql.NullString
		err := db.QueryRowContext(ctx, "SELECT TABLE_TYPE, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", name, table.Name).Scan(&kind, &storageEngine)
		if errors.Is(err, sql.ErrNoRows) && table.Optional {
			continue
		}
		if errors.Is(err, sql.ErrNoRows) {
			return plan, report.Fail(report.Schema, "required table does not exist")
		}
		if err != nil {
			return plan, err
		}
		if kind != "BASE TABLE" {
			return plan, report.Fail(report.Schema, "policy target is not a base table")
		}
		if table.Action != api.TableActionTruncate && storageEngine.String != "InnoDB" {
			return plan, report.Fail(report.Schema, "anonymization requires InnoDB for atomic page checkpoints")
		}
		item, err := planTable(ctx, db, name, table, defaults, options)
		if err != nil {
			return plan, err
		}
		plan.tables = append(plan.tables, item)
	}
	return plan, nil
}

func planTable(ctx context.Context, db *sql.DB, database string, rule api.TablePolicy, defaults *api.PolicyDefaults, options Options) (tablePlan, error) {
	plan := tablePlan{page: pager.Table{Database: database, Name: rule.Name, PageSize: options.PageSize}, action: rule.Action}
	if _, err := pager.QuoteIdentifier(rule.Name); err != nil {
		return plan, report.Fail(report.Schema, "table name is not a supported SQL identifier")
	}
	if rule.Action == api.TableActionTruncate {
		return plan, nil
	}
	metadata, err := readColumns(ctx, db, database, rule.Name)
	if err != nil {
		return plan, err
	}
	indexes, err := readIndexes(ctx, db, database, rule.Name)
	if err != nil {
		return plan, err
	}
	key, err := primaryKey(rule.PrimaryKey, metadata, indexes)
	if err != nil {
		return plan, err
	}
	plan.page.PrimaryKey = key
	columnDefaults := api.PolicyDefaults{}
	if defaults != nil {
		columnDefaults = *defaults
		if defaults.PageSize > 0 {
			plan.page.PageSize = int(defaults.PageSize)
		}
	}
	if rule.PageSize != nil {
		plan.page.PageSize = int(*rule.PageSize)
	}
	if rule.Ignore != nil {
		plan.page.IgnoreWhere = rule.Ignore.Where
	}
	for _, column := range rule.Columns {
		info, found := metadata[strings.ToLower(column.Name)]
		if !found || info.generated {
			return plan, report.Fail(report.Schema, "policy column is missing or generated")
		}
		if err := compatible(column, info, options.ReferenceTime); err != nil {
			return plan, err
		}
		constant, err := constantValue(column, options.ConstantsDir)
		if err != nil {
			return plan, report.Fail(report.Policy, "constant projected file cannot be resolved")
		}
		compiled, err := strategy.Compile(column, columnDefaults, constant)
		if err != nil {
			return plan, report.Fail(report.Policy, "strategy parameters are invalid")
		}
		if length, known := compiled.OutputLength(); known && info.length.Valid && int64(length) > info.length.Int64 {
			return plan, report.Fail(report.Schema, "column capacity is below the strategy output length")
		}
		if err := validateKnownValues(column, constant, info); err != nil {
			return plan, err
		}
		plan.page.Columns = append(plan.page.Columns, info.name)
		plan.columns = append(plan.columns, compiled)
		plan.metadata = append(plan.metadata, info)
	}
	if err := pager.Validate(plan.page); err != nil {
		return plan, report.Fail(report.Schema, "primary key mutation or invalid column identifiers")
	}
	return plan, nil
}

func readColumns(ctx context.Context, db *sql.DB, database, table string) (map[string]columnInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, CHARACTER_MAXIMUM_LENGTH, IS_NULLABLE, EXTRA
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]columnInfo{}
	for rows.Next() {
		var column columnInfo
		var nullable, extra string
		if err := rows.Scan(&column.name, &column.kind, &column.fullType, &column.length, &nullable, &extra); err != nil {
			return nil, err
		}
		column.nullable = nullable == "YES"
		column.generated = strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED")
		columns[strings.ToLower(column.name)] = column
	}
	return columns, rows.Err()
}

func readIndexes(ctx context.Context, db *sql.DB, database, table string) (map[string]indexInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	indexes := map[string]indexInfo{}
	for rows.Next() {
		var name string
		var column sql.NullString
		var nonUnique int
		var prefix sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &column, &prefix); err != nil {
			return nil, err
		}
		index, exists := indexes[name]
		if !exists {
			index.full = true
		}
		index.unique = nonUnique == 0
		index.full = index.full && column.Valid && !prefix.Valid
		index.names = append(index.names, column.String)
		indexes[name] = index
	}
	return indexes, rows.Err()
}

func primaryKey(override []string, columns map[string]columnInfo, indexes map[string]indexInfo) ([]string, error) {
	key := slices.Clone(override)
	if len(key) == 0 {
		key = indexes["PRIMARY"].names
	}
	if len(key) == 0 {
		return nil, report.Fail(report.Schema, "TableWithoutPrimaryKey: table requires a primary or declared unique key")
	}
	for i, name := range key {
		column, found := columns[strings.ToLower(name)]
		if !found || column.nullable {
			return nil, report.Fail(report.Schema, "paging key must contain existing non-null columns")
		}
		key[i] = column.name
	}
	for _, index := range indexes {
		if index.unique && index.full && slices.Equal(index.names, key) {
			return key, nil
		}
	}
	return nil, report.Fail(report.Schema, "paging key must match a complete unique index")
}

func compatible(rule api.ColumnRule, column columnInfo, reference time.Time) error {
	if rule.Strategy == api.StrategyNull {
		if !column.nullable {
			return report.Fail(report.Schema, "null strategy requires a nullable column")
		}
		return nil
	}
	text := slices.Contains([]string{"char", typeVarchar, "tinytext", "text", "mediumtext", "longtext", "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob"}, column.kind)
	switch rule.Strategy {
	case api.StrategyDate:
		if !text && column.kind != typeDate && column.kind != typeDateTime && column.kind != typeTimestamp {
			return report.Fail(report.Schema, "date strategy requires a date, datetime or text column")
		}
		if rule.Params != nil && rule.Params.To == "" && rule.Params.From != "" {
			from, err := time.Parse(time.DateOnly, rule.Params.From)
			if err != nil || from.After(reference.UTC().Truncate(24*time.Hour)) {
				return report.Fail(report.Policy, "date range starts after the fixed reference date")
			}
		}
	case api.StrategyNumber:
		if !text && !slices.Contains([]string{typeTinyInt, typeSmallInt, typeMediumInt, typeInt, typeBigInt, typeDecimal, typeFloat, typeDouble}, column.kind) {
			return report.Fail(report.Schema, "number strategy requires a numeric or text column")
		}
	case api.StrategyConstant:
		if !text && !slices.Contains([]string{typeTinyInt, typeSmallInt, typeMediumInt, typeInt, typeBigInt, typeDecimal, typeFloat, typeDouble, typeDate, typeDateTime, typeTimestamp}, column.kind) {
			return report.Fail(report.Schema, "strategy is incompatible with the column type")
		}
	default:
		if !text {
			return report.Fail(report.Schema, "text strategy requires a text or binary column")
		}
	}
	if column.length.Valid {
		minimum := int64(1)
		if rule.Strategy == api.StrategyEmail {
			minimum = 32
		}
		if rule.Params != nil && rule.Params.Length != nil {
			minimum = int64(*rule.Params.Length)
		}
		if column.length.Int64 < minimum {
			return report.Fail(report.Schema, fmt.Sprintf("column capacity is below strategy minimum %d", minimum))
		}
	}
	return nil
}

func validateKnownValues(rule api.ColumnRule, constant *string, column columnInfo) error {
	if rule.Strategy == api.StrategyConstant {
		if rule.Params != nil && rule.Params.Value != nil {
			constant = rule.Params.Value
		}
		if constant != nil {
			return validateValue(*constant, column)
		}
	}
	if rule.Strategy == api.StrategyNumber {
		minimum, maximum := int64(0), int64(100)
		if rule.Params != nil {
			if rule.Params.Min != nil {
				minimum = *rule.Params.Min
			}
			if rule.Params.Max != nil {
				maximum = *rule.Params.Max
			}
		}
		if err := validateValue(minimum, column); err != nil {
			return err
		}
		return validateValue(maximum, column)
	}
	return nil
}
