// Package pager updates keyset pages and their resume cursor atomically.
package pager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
)

const MaxUniqueAttempts = 8

var identifier = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)

type Table struct {
	Database, Name      string
	PrimaryKey, Columns []string
	IgnoreWhere         string
	PageSize            int
}

type Row struct {
	PrimaryKey, Values []any
	Retry              uint8
}

func QuoteIdentifier(name string) (string, error) {
	if !identifier.MatchString(name) {
		return "", errors.New("invalid SQL identifier")
	}
	return "`" + name + "`", nil
}

func Validate(table Table) error {
	if len(table.PrimaryKey) == 0 || len(table.Columns) == 0 || table.PageSize < 0 {
		return errors.New("page requires primary key, policy columns and a nonnegative page size")
	}
	for _, names := range [][]string{{table.Database, table.Name}, table.PrimaryKey, table.Columns} {
		for _, name := range names {
			if _, err := QuoteIdentifier(name); err != nil {
				return err
			}
		}
	}
	seen := make(map[string]bool, len(table.PrimaryKey)+len(table.Columns))
	for _, name := range append(append([]string{}, table.PrimaryKey...), table.Columns...) {
		folded := strings.ToLower(name)
		if seen[folded] {
			return errors.New("duplicate columns or primary key mutation are not supported")
		}
		seen[folded] = true
	}
	return nil
}

func ProcessPage(ctx context.Context, conn *sql.Conn, table Table, key checkpoint.Key,
	transform func(context.Context, Row) ([]any, error),
) (checkpoint.Progress, error) {
	if err := Validate(table); err != nil {
		return checkpoint.Progress{}, err
	}
	if transform == nil || key.Kind != "table" || key.Database != table.Database || key.Name != table.Name {
		return checkpoint.Progress{}, errors.New("page identity or transform is invalid")
	}
	if table.PageSize == 0 {
		table.PageSize = 5000
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Progress{}, err
	}
	defer func() { _ = tx.Rollback() }()
	progress, err := checkpoint.Load(ctx, tx, key)
	if err != nil || progress.Done {
		return progress, err
	}
	if len(progress.LastPK) != 0 && len(progress.LastPK) != len(table.PrimaryKey) {
		return progress, errors.New("checkpoint primary key shape differs from this table")
	}
	query, args := selectSQL(table, progress.LastPK)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return progress, err
	}
	page, err := readRows(rows, len(table.PrimaryKey), len(table.Columns))
	if err != nil {
		return progress, err
	}
	if len(page) > 0 {
		statement, prepareErr := tx.PrepareContext(ctx, updateSQL(table))
		if prepareErr != nil {
			return progress, prepareErr
		}
		defer func() { _ = statement.Close() }()
		for _, row := range page {
			if err := updateRow(ctx, statement, row, len(table.Columns), transform); err != nil {
				return progress, err
			}
		}
		progress.LastPK = page[len(page)-1].PrimaryKey
		progress.RowsDone += int64(len(page))
	}
	progress.Done = len(page) < table.PageSize
	if err := checkpoint.Save(ctx, tx, key, progress); err != nil {
		return progress, err
	}
	if err := tx.Commit(); err != nil {
		return progress, err
	}
	progress.Exists = true
	return progress, nil
}

func readRows(rows *sql.Rows, keys, columns int) ([]Row, error) {
	defer func() { _ = rows.Close() }()
	var page []Row
	for rows.Next() {
		values := make([]any, keys+columns)
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		page = append(page, Row{PrimaryKey: values[:keys], Values: values[keys:]})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, rows.Close()
}

func updateRow(ctx context.Context, statement *sql.Stmt, row Row, columns int,
	transform func(context.Context, Row) ([]any, error),
) error {
	for attempt := range MaxUniqueAttempts {
		row.Retry = uint8(attempt)
		values, err := transform(ctx, row)
		if err != nil {
			return err
		}
		if len(values) != columns {
			return errors.New("transform returned an incorrect number of columns")
		}
		_, err = statement.ExecContext(ctx, append(values, row.PrimaryKey...)...)
		if err == nil {
			return nil
		}
		var mysqlError *mysql.MySQLError
		if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 {
			return err
		}
	}
	return &mysql.MySQLError{Number: 1062, Message: "unique constraint retry limit reached"}
}

func quoted(names []string) string {
	return "`" + strings.Join(names, "`,`") + "`"
}

func selectSQL(table Table, cursor []any) (string, []any) {
	query := "SELECT " + quoted(table.PrimaryKey) + "," + quoted(table.Columns) +
		" FROM " + quoted([]string{table.Database}) + "." + quoted([]string{table.Name})
	var predicates []string
	args := append([]any{}, cursor...)
	if len(cursor) != 0 {
		predicates = append(predicates, "("+quoted(table.PrimaryKey)+") > ("+placeholders(len(cursor))+")")
	}
	if table.IgnoreWhere != "" {
		predicates = append(predicates, "NOT ("+table.IgnoreWhere+")")
	}
	if len(predicates) > 0 {
		query += " WHERE " + strings.Join(predicates, " AND ")
	}
	return query + " ORDER BY " + quoted(table.PrimaryKey) + " LIMIT ?", append(args, table.PageSize)
}

func updateSQL(table Table) string {
	assignments := make([]string, len(table.Columns))
	for i, name := range table.Columns {
		assignments[i] = "`" + name + "`=?"
	}
	return fmt.Sprintf("UPDATE `%s`.`%s` SET %s WHERE (%s) = (%s)", table.Database, table.Name,
		strings.Join(assignments, ","), quoted(table.PrimaryKey), placeholders(len(table.PrimaryKey)))
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
