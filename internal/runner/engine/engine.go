// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
	"github.com/ydixken/pxc-anonymizer/internal/runner/pager"
	"github.com/ydixken/pxc-anonymizer/internal/runner/policy"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
	"github.com/ydixken/pxc-anonymizer/internal/runner/sqlstep"
	"github.com/ydixken/pxc-anonymizer/internal/runner/strategy"
)

const checkpointStep = "step"
const logMessageKey = "msg"

type Options struct {
	PolicyJSON       []byte
	Seed             []byte
	RunUID           string
	ReferenceTime    time.Time
	Workers          int
	PageSize         int
	DisableBinlog    bool
	DryRun           bool
	StepsDir         string
	ConstantsDir     string
	ProgressInterval time.Duration
	Progress         io.Writer
}

type execution struct {
	options Options
	result  report.Report
	mu      sync.Mutex
	rows    map[string]int64
	lastLog time.Time
	faker   *strategy.Generator
}

func Run(ctx context.Context, db *sql.DB, options Options) report.Report {
	started := time.Now()
	e := &execution{options: options, result: report.Report{Result: report.ResultOK, Message: "anonymization completed"}, rows: map[string]int64{}}
	err := e.run(ctx, db)
	if err != nil {
		e.result.Result = report.ResultError
		e.result.Class, e.result.Message = report.Classify(err)
	}
	e.result.DurationSeconds = time.Since(started).Seconds()
	return e.result
}

func (e *execution) run(ctx context.Context, db *sql.DB) error {
	if db == nil || e.options.RunUID == "" || len(e.options.RunUID) > 128 || e.options.ReferenceTime.IsZero() {
		return report.Fail(report.Policy, "database, Run UID and fixed reference time are required")
	}
	if e.options.Workers == 0 {
		e.options.Workers = 4
	}
	if e.options.PageSize == 0 {
		e.options.PageSize = 5000
	}
	if e.options.Workers < 1 || e.options.Workers > 32 || e.options.PageSize < 1 || e.options.PageSize > 50000 {
		return report.Fail(report.Policy, "workers must be 1..32 and page size 1..50000")
	}
	if e.options.ProgressInterval <= 0 {
		e.options.ProgressInterval = 10 * time.Second
	}
	spec, hash, err := policy.Decode(e.options.PolicyJSON)
	if err != nil {
		return report.Fail(report.Policy, "policy snapshot validation failed")
	}
	e.result.PolicyHash = hash
	e.faker, err = strategy.New(e.options.Seed, hash, e.options.ReferenceTime)
	if err != nil {
		return report.Fail(report.Policy, "determinism seed or reference time is invalid")
	}
	if err := connect(ctx, db); err != nil {
		return err
	}
	plan, err := preflight(ctx, db, spec, e.options)
	if err != nil {
		return err
	}
	e.result.TablesTotal = plan.total
	if e.options.DryRun {
		e.result.Message = "dry-run preflight completed without writes"
		return nil
	}
	control, err := e.connection(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = control.Close() }()
	if err := checkpoint.Ensure(ctx, control); err != nil {
		return err
	}
	definitions := make(map[string]api.SQLStep, len(spec.Steps))
	for _, step := range spec.Steps {
		definitions[step.Name] = step
	}
	for _, database := range plan.databases {
		if err := e.steps(ctx, control, database, "pre", database.policy.Pre, definitions, plan.steps); err != nil {
			return err
		}
		group, tableContext := errgroup.WithContext(ctx)
		group.SetLimit(e.options.Workers)
		for _, table := range database.tables {
			group.Go(func() error { return e.table(tableContext, db, table) })
		}
		if err := group.Wait(); err != nil {
			return err
		}
		if err := e.steps(ctx, control, database, "post", database.policy.Post, definitions, plan.steps); err != nil {
			return err
		}
	}
	if err := checkpoint.Drop(ctx, control); err != nil {
		return report.Fail(report.Policy, "checkpoint cleanup outcome is uncertain; automatic retry is unsafe")
	}
	return nil
}

func connect(ctx context.Context, db *sql.DB) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	delay := time.Second
	for {
		err := db.PingContext(deadline)
		if err == nil {
			return nil
		}
		class, _ := report.Classify(err)
		if class != report.Transient {
			return err
		}
		if err := pause(deadline, delay); err != nil {
			return err
		}
		delay = min(delay*2, 10*time.Second)
	}
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *execution) connection(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if e.options.DisableBinlog {
		if _, err := conn.ExecContext(ctx, "SET SESSION sql_log_bin=0"); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	// A permissive server must not silently truncate or coerce replacement values.
	if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode=CONCAT_WS(',',@@sql_mode,'STRICT_ALL_TABLES')"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (e *execution) key(database, name, kind string) checkpoint.Key {
	return checkpoint.Key{RunUID: e.options.RunUID, Database: database, Name: name, Kind: kind, PolicyHash: e.result.PolicyHash}
}

func (e *execution) steps(ctx context.Context, conn *sql.Conn, database databasePlan, phase string, refs []api.StepRef, definitions map[string]api.SQLStep, bodies map[string]string) error {
	for i, ref := range refs {
		key := e.key(database.name, fmt.Sprintf("%s:%d:%s", phase, i, ref.Name), checkpointStep)
		executed, err := sqlstep.Run(ctx, conn, database.name, bodies[ref.Name], key, false)
		entry := map[string]any{logMessageKey: "ok", checkpointStep: phase, "db": database.name, "name": ref.Name}
		if err != nil {
			entry[logMessageKey] = "error"
			entry["class"], entry["message"] = report.Classify(err)
		}
		e.log(entry)
		if err != nil && (errors.Is(err, sqlstep.ErrUncertainOutcome) || !definitions[ref.Name].ContinueOnError) {
			return err
		}
		if executed || err == nil {
			e.result.StepsDone++
		}
	}
	return nil
}

func (e *execution) table(ctx context.Context, db *sql.DB, plan tablePlan) error {
	if plan.action == api.TableActionTruncate {
		conn, err := e.connection(ctx, db)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if err := e.truncate(ctx, conn, plan); err != nil {
			return err
		}
		e.progress(plan, checkpoint.Progress{Done: true})
		return nil
	}
	var conn *sql.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	attempt := 0
	for {
		var err error
		if conn == nil {
			conn, err = e.connection(ctx, db)
		}
		var progress checkpoint.Progress
		if err == nil {
			progress, err = pager.ProcessPage(ctx, conn, plan.page, e.key(plan.page.Database, plan.page.Name, "table"), e.transform(plan))
		}
		if err == nil {
			attempt = 0
			e.progress(plan, progress)
			if progress.Done {
				return nil
			}
			continue
		}
		class, _ := report.Classify(err)
		if class != report.Transient || attempt == 3 {
			return err
		}
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}
		if err := pause(ctx, time.Second<<attempt); err != nil {
			return err
		}
		attempt++
	}
}

func (e *execution) transform(plan tablePlan) func(context.Context, pager.Row) ([]any, error) {
	return func(ctx context.Context, row pager.Row) ([]any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		primaryKey, err := json.Marshal(row.PrimaryKey)
		if err != nil {
			return nil, report.Fail(report.Schema, "primary key cannot be encoded")
		}
		values := make([]any, len(plan.columns))
		for i, column := range plan.columns {
			identity := strategy.Row{Database: plan.page.Database, Table: plan.page.Name, PrimaryKey: string(primaryKey), Column: plan.page.Columns[i]}
			values[i], err = e.faker.Generate(column, row.Values[i], identity, row.Retry)
			if err != nil {
				return nil, report.Fail(report.Policy, "strategy generation failed")
			}
			if err := validateValue(values[i], plan.metadata[i]); err != nil {
				return nil, err
			}
		}
		return values, nil
	}
}

func (e *execution) truncate(ctx context.Context, conn *sql.Conn, plan tablePlan) (err error) {
	database, _ := pager.QuoteIdentifier(plan.page.Database)
	table, _ := pager.QuoteIdentifier(plan.page.Name)
	body := "SET FOREIGN_KEY_CHECKS=0; TRUNCATE TABLE " + database + "." + table + "; SET FOREIGN_KEY_CHECKS=1"
	defer func() {
		resetContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, resetErr := conn.ExecContext(resetContext, "SET FOREIGN_KEY_CHECKS=1"); resetErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, resetErr)
		}
	}()
	_, err = sqlstep.Run(ctx, conn, plan.page.Database, body, e.key(plan.page.Database, "truncate:"+plan.page.Name, checkpointStep), false)
	return err
}

func (e *execution) progress(plan tablePlan, progress checkpoint.Progress) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := plan.page.Database + "." + plan.page.Name
	e.result.RowsDone += progress.RowsDone - e.rows[key]
	e.rows[key] = progress.RowsDone
	if progress.Done {
		e.result.TablesDone++
	}
	if progress.Done || time.Since(e.lastLog) >= e.options.ProgressInterval {
		e.lastLog = time.Now()
		e.writeLog(map[string]any{logMessageKey: "progress", "db": plan.page.Database, "table": plan.page.Name,
			"tablesDone": e.result.TablesDone, "tablesTotal": e.result.TablesTotal, "rowsDone": e.result.RowsDone, "stepsDone": e.result.StepsDone})
	}
}

func (e *execution) log(entry map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writeLog(entry)
}

func (e *execution) writeLog(entry map[string]any) {
	if e.options.Progress != nil {
		_ = json.NewEncoder(e.options.Progress).Encode(entry)
	}
}
