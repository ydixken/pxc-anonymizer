// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

const testDatabase = "demo"
const testPayload = "payload"
const testTenant = "tenant"
const testProjectionDirectory = "..data"
const testSQLFile = "prepare.sql"
const testSequence = "sequence"

type metadataState struct {
	noKey         bool
	extra         string
	storageEngine string
	columnLength  int64
	dropError     error
	drops         atomic.Int32
	writes        atomic.Int32
	queries       atomic.Int32
}

type metadataConnector struct{ state *metadataState }
type metadataDriver struct{}
type metadataConn struct{ state *metadataState }
type metadataRows struct {
	columns []string
	values  [][]driver.Value
}

func (c metadataConnector) Connect(context.Context) (driver.Conn, error) {
	return &metadataConn{state: c.state}, nil
}
func (metadataConnector) Driver() driver.Driver { return metadataDriver{} }
func (metadataDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the explicit test connector")
}
func (*metadataConn) Close() error { return nil }
func (*metadataConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*metadataConn) Begin() (driver.Tx, error)  { return nil, errors.New("unexpected transaction") }
func (*metadataConn) Ping(context.Context) error { return nil }
func (c *metadataConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.writes.Add(1)
	if c.state.dropError != nil {
		if strings.HasPrefix(query, "DROP SCHEMA") {
			c.state.drops.Add(1)
			return nil, c.state.dropError
		}
		return driver.RowsAffected(0), nil
	}
	return nil, errors.New("unexpected write")
}
func (c *metadataConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries.Add(1)
	switch {
	case strings.Contains(query, "information_schema.SCHEMATA"):
		return &metadataRows{columns: []string{"SCHEMA_NAME"}, values: [][]driver.Value{{testDatabase}}}, nil
	case strings.Contains(query, "information_schema.TABLES"):
		storageEngine := c.state.storageEngine
		if storageEngine == "" {
			storageEngine = "InnoDB"
		}
		return &metadataRows{columns: []string{"TABLE_TYPE", "ENGINE"}, values: [][]driver.Value{{"BASE TABLE", storageEngine}}}, nil
	case strings.Contains(query, "information_schema.COLUMNS"):
		length := c.state.columnLength
		if length == 0 {
			length = 255
		}
		return &metadataRows{columns: []string{"COLUMN_NAME", "DATA_TYPE", "COLUMN_TYPE", "CHARACTER_MAXIMUM_LENGTH", "IS_NULLABLE", "EXTRA"}, values: [][]driver.Value{
			{"id", typeBigInt, typeBigInt, nil, "NO", ""},
			{testPayload, typeVarchar, "varchar(255)", length, "YES", c.state.extra},
		}}, nil
	case strings.Contains(query, "information_schema.STATISTICS"):
		rows := &metadataRows{columns: []string{"INDEX_NAME", "NON_UNIQUE", "COLUMN_NAME", "SUB_PART"}}
		if !c.state.noKey {
			rows.values = [][]driver.Value{{"PRIMARY", int64(0), "id", nil}}
		}
		return rows, nil
	default:
		return nil, errors.New("unexpected metadata query")
	}
}
func (r *metadataRows) Columns() []string { return r.columns }
func (*metadataRows) Close() error        { return nil }
func (r *metadataRows) Next(values []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(values, r.values[0])
	r.values = r.values[1:]
	return nil
}

func testOptions(t *testing.T, column api.ColumnRule) Options {
	t.Helper()
	spec := api.AnonymizationPolicySpec{Databases: []api.DatabasePolicy{{Name: testDatabase, Tables: []api.TablePolicy{{Name: "people", Columns: []api.ColumnRule{column}}}}}}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return Options{PolicyJSON: data, Seed: bytes.Repeat([]byte{'A'}, 32), RunUID: "unit-run", ReferenceTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Workers: 2, PageSize: 10}
}

func TestPreflightFailureBeforeEveryWrite(t *testing.T) {
	for _, test := range []struct {
		name          string
		noKey         bool
		storageEngine string
		columnLength  int64
		rule          api.ColumnRule
	}{
		{name: "missing primary key", noKey: true, rule: api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash}},
		{name: "primary key mutation", rule: api.ColumnRule{Name: "id", Strategy: api.StrategyNumber}},
		{name: "future date bound", rule: api.ColumnRule{Name: testPayload, Strategy: api.StrategyDate, Params: &api.StrategyParams{From: "2030-01-01"}}},
		{name: "nontransactional storage", storageEngine: "MyISAM", rule: api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash}},
		{name: "default hash exceeds capacity", columnLength: 32, rule: api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash}},
		{name: "base64 hash exceeds capacity", columnLength: 43, rule: api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash, Params: &api.StrategyParams{Encoding: "base64"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &metadataState{noKey: test.noKey, storageEngine: test.storageEngine, columnLength: test.columnLength}
			db := sql.OpenDB(metadataConnector{state: state})
			defer func() { _ = db.Close() }()
			result := Run(context.Background(), db, testOptions(t, test.rule))
			if result.Result != report.ResultError || (result.Class != report.Schema && result.Class != report.Policy) {
				t.Fatalf("missing preflight rejection: %+v", result)
			}
			if state.writes.Load() != 0 || state.queries.Load() == 0 {
				t.Fatalf("preflight did not precede writes: queries=%d writes=%d", state.queries.Load(), state.writes.Load())
			}
		})
	}
}

func TestDryRunOnlyReadsMetadata(t *testing.T) {
	state := &metadataState{}
	db := sql.OpenDB(metadataConnector{state: state})
	defer func() { _ = db.Close() }()
	options := testOptions(t, api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash})
	options.DryRun, options.DisableBinlog = true, true
	result := Run(context.Background(), db, options)
	if result.ExitCode() != 0 || result.TablesTotal != 1 || result.TablesDone != 0 || result.RowsDone != 0 {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if state.writes.Load() != 0 || state.queries.Load() < 4 {
		t.Fatalf("dry run did not remain read-only: queries=%d writes=%d", state.queries.Load(), state.writes.Load())
	}
}

func TestCheckpointDropFailureIsAbsorbing(t *testing.T) {
	const sensitive = "sensitive-database-error"
	state := &metadataState{dropError: errors.New(sensitive)}
	db := sql.OpenDB(metadataConnector{state: state})
	defer func() { _ = db.Close() }()
	options := testOptions(t, api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash})
	options.PolicyJSON = []byte(`{"databases":[{"name":"demo","tables":[]}]}`)
	result := Run(context.Background(), db, options)
	if state.drops.Load() != 1 || result.Class != report.Policy || result.ExitCode() != 10 ||
		strings.Contains(result.Message, sensitive) {
		t.Fatalf("uncertain checkpoint cleanup was retriable or unsanitized: %+v", result)
	}
}

func TestEngineWorkerBounds(t *testing.T) {
	for _, workers := range []int{-1, 1, 32, 33} {
		state := &metadataState{}
		db := sql.OpenDB(metadataConnector{state: state})
		options := testOptions(t, api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash})
		options.Workers, options.DryRun = workers, true
		result := Run(context.Background(), db, options)
		_ = db.Close()
		valid := workers >= 1 && workers <= 32
		if (result.ExitCode() == 0) != valid || (!valid && result.Class != report.Policy) {
			t.Fatalf("worker boundary %d: %+v", workers, result)
		}
		if state.writes.Load() != 0 || (!valid && state.queries.Load() != 0) {
			t.Fatal("worker validation did not precede database access")
		}
	}
}

func TestGeneratedColumnClassification(t *testing.T) {
	for _, extra := range []string{"DEFAULT_GENERATED", "VIRTUAL GENERATED", "STORED GENERATED"} {
		state := &metadataState{extra: extra}
		db := sql.OpenDB(metadataConnector{state: state})
		options := testOptions(t, api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash})
		options.DryRun = true
		result := Run(context.Background(), db, options)
		_ = db.Close()
		if extra == "DEFAULT_GENERATED" {
			if result.ExitCode() != 0 {
				t.Fatal("writable default-expression column was rejected")
			}
		} else if result.Class != report.Schema {
			t.Fatal("computed column was accepted")
		}
		if state.writes.Load() != 0 || state.queries.Load() == 0 {
			t.Fatal("classification did not stay in read-only preflight")
		}
	}
}

func TestProjectedPathsAllowKubernetesLinksAndRejectTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, testProjectionDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, testProjectionDirectory, testSQLFile), []byte("SELECT 1;\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(testProjectionDirectory, testSQLFile), filepath.Join(root, testSQLFile)); err != nil {
		t.Fatal(err)
	}
	path, err := projectedPath(root, testSQLFile)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(path, 1024)
	if err != nil || string(data) != "SELECT 1;\n" {
		t.Fatal("legitimate projected file was changed or rejected")
	}
	for _, invalid := range []string{"..", ".", "../outside", "dir/file", "dir\\file", "/absolute"} {
		if _, err := projectedPath(root, invalid); err == nil {
			t.Fatalf("accepted unsafe component %q", invalid)
		}
	}
}

func TestPagingKeyAndValueSafety(t *testing.T) {
	columns := map[string]columnInfo{testTenant: {name: testTenant}, testSequence: {name: testSequence}}
	indexes := map[string]indexInfo{"unique_identity": {names: []string{testTenant, testSequence}, unique: true, full: true}}
	if _, err := primaryKey([]string{testTenant, testSequence}, columns, indexes); err != nil {
		t.Fatal("complete non-id composite unique key rejected:", err)
	}
	if _, err := primaryKey([]string{testTenant}, columns, indexes); err == nil {
		t.Fatal("partial unique index accepted")
	}
	for _, sample := range []struct {
		value any
		info  columnInfo
	}{
		{nil, columnInfo{nullable: false}},
		{"128", columnInfo{kind: typeTinyInt, fullType: typeTinyInt}},
		{"-1", columnInfo{kind: typeInt, fullType: "int unsigned"}},
		{"1.234", columnInfo{kind: typeDecimal, fullType: "decimal(5,2)"}},
		{"not a date", columnInfo{kind: typeDate}},
		{"abc", columnInfo{kind: typeVarchar, length: sql.NullInt64{Int64: 2, Valid: true}}},
	} {
		if err := validateValue(sample.value, sample.info); err == nil {
			t.Fatal("unsafe replacement accepted")
		}
	}
}
