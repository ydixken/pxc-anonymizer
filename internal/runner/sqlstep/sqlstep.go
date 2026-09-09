// Package sqlstep executes server-parsed SQL and refuses uncertain step replay.
package sqlstep

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
	"github.com/ydixken/pxc-anonymizer/internal/runner/pager"
)

var ErrUncertainOutcome = errors.New("SQL step was interrupted; its committed outcome requires explicit recovery")

// Execute requires a connection configured with MySQL multiStatements=true.
func Execute(ctx context.Context, conn *sql.Conn, database, body string, dryRun bool) error {
	quoted, err := pager.QuoteIdentifier(database)
	if err != nil {
		return err
	}
	if body == "" {
		return errors.New("SQL step body is empty")
	}
	if dryRun {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "USE "+quoted); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, body)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(pointers...); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !rows.NextResultSet() {
			if err := rows.Err(); err != nil {
				return err
			}
			return rows.Close()
		}
	}
}

// DDL can commit implicitly; a durable intent prevents replay after an uncertain exit.
func Run(ctx context.Context, conn *sql.Conn, database, body string, key checkpoint.Key, dryRun bool) (bool, error) {
	if key.Kind != "step" || key.Database != database {
		return false, errors.New("SQL step checkpoint identity is invalid")
	}
	if dryRun {
		return false, Execute(ctx, conn, database, body, true)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	progress, err := checkpoint.Load(ctx, tx, key)
	if err != nil {
		return false, err
	}
	if progress.Exists {
		if progress.Done {
			return false, nil
		}
		return false, ErrUncertainOutcome
	}
	if err := checkpoint.Save(ctx, tx, key, checkpoint.Progress{}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if err := Execute(ctx, conn, database, body, false); err != nil {
		return false, errors.Join(ErrUncertainOutcome, err)
	}
	complete, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.Join(ErrUncertainOutcome, err)
	}
	defer func() { _ = complete.Rollback() }()
	if err := checkpoint.Save(ctx, complete, key, checkpoint.Progress{Done: true}); err != nil {
		return false, errors.Join(ErrUncertainOutcome, err)
	}
	if err := complete.Commit(); err != nil {
		return false, errors.Join(ErrUncertainOutcome, err)
	}
	return true, nil
}

func Truncate(ctx context.Context, conn *sql.Conn, database, table string, dryRun bool) (err error) {
	db, err := pager.QuoteIdentifier(database)
	if err != nil {
		return err
	}
	name, err := pager.QuoteIdentifier(table)
	if err != nil || dryRun {
		return err
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return err
	}
	defer func() {
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, resetErr := conn.ExecContext(resetCtx, "SET FOREIGN_KEY_CHECKS=1"); resetErr != nil {
			// A pooled connection must never retain disabled constraint checks.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, resetErr)
		}
	}()
	_, err = conn.ExecContext(ctx, "TRUNCATE TABLE "+db+"."+name)
	return err
}
