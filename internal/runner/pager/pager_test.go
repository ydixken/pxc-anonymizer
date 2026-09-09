package pager

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
)

const pagerTestTable = "items"
const pagerTestDatabase = "sample"

func TestKeysetQuery(t *testing.T) {
	table := Table{Database: pagerTestDatabase, Name: pagerTestTable, PrimaryKey: []string{"tenant", "sequence"},
		Columns: []string{"email"}, IgnoreWhere: "protected = 1", PageSize: 2}
	if err := Validate(table); err != nil {
		t.Fatal(err)
	}
	query, args := selectSQL(table, []any{[]byte("a"), int64(9)})
	for _, fragment := range []string{"(`tenant`,`sequence`) > (?,?)", "NOT (protected = 1)",
		"ORDER BY `tenant`,`sequence` LIMIT ?"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("missing keyset constraint %q in %s", fragment, query)
		}
	}
	if strings.Contains(query, "SELECT *") || len(args) != 3 || args[2] != 2 {
		t.Fatalf("invalid projection or bindings: %s %#v", query, args)
	}
}

func TestUniqueRetriesStayOnCollidedRow(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		name := "last attempt succeeds"
		if exhaust {
			name = "eight attempts exhausted"
		}
		t.Run(name, func(t *testing.T) {
			state := &uniqueState{exhaust: exhaust}
			db := sql.OpenDB(uniqueConnector{state})
			defer func() { _ = db.Close() }()
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			calls := map[int64][]uint8{}
			transform := func(_ context.Context, row Row) ([]any, error) {
				key := row.PrimaryKey[0].(int64)
				calls[key] = append(calls[key], row.Retry)
				return []any{int64(row.Retry)}, nil
			}
			table := Table{Database: pagerTestDatabase, Name: pagerTestTable, PrimaryKey: []string{"id"},
				Columns: []string{"payload"}, PageSize: 3}
			progress, err := ProcessPage(t.Context(), conn, table,
				checkpoint.Key{RunUID: "unique-test", Kind: "table", Database: table.Database,
					Name: table.Name, PolicyHash: "sha256:test"}, transform)
			if !slices.Equal(calls[1], []uint8{0}) || !slices.Equal(calls[2], []uint8{0, 1, 2, 3, 4, 5, 6, 7}) {
				t.Fatalf("unexpected transformed rows or retry counters: %v; error: %v", calls, err)
			}
			if state.updates != 9 {
				t.Fatalf("executed %d updates, want one successful row plus eight collided-row attempts", state.updates)
			}
			if exhaust {
				var collision *mysql.MySQLError
				if !errors.As(err, &collision) || collision.Number != 1062 || strings.Contains(err.Error(), uniqueSensitive) {
					t.Fatalf("exhaustion must return a sanitized MySQL1062: %v", err)
				}
				if state.committed || state.checkpoints != 0 || !state.rolledBack {
					t.Fatal("exhausted page did not roll back without a checkpoint")
				}
			} else if err != nil || !progress.Done || progress.RowsDone != 2 || !state.committed || state.checkpoints != 1 {
				t.Fatalf("successful last retry did not commit the complete page: %+v, %v", progress, err)
			}
		})
	}
}

const uniqueSensitive = "private-collision-value"

type uniqueState struct {
	exhaust, committed, rolledBack bool
	updates, checkpoints           int
}

type uniqueConnector struct{ state *uniqueState }
type uniqueDriver struct{}
type uniqueConn struct{ state *uniqueState }
type uniqueTx struct{ state *uniqueState }
type uniqueStmt struct{ state *uniqueState }
type uniqueRows struct {
	columns []string
	values  [][]driver.Value
}

func (c uniqueConnector) Connect(context.Context) (driver.Conn, error) {
	return &uniqueConn{c.state}, nil
}
func (uniqueConnector) Driver() driver.Driver { return uniqueDriver{} }
func (uniqueDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the explicit unique-test connector")
}
func (*uniqueConn) Close() error                          { return nil }
func (c *uniqueConn) Begin() (driver.Tx, error)           { return &uniqueTx{c.state}, nil }
func (c *uniqueConn) Prepare(string) (driver.Stmt, error) { return &uniqueStmt{c.state}, nil }
func (c *uniqueConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "_pxc_anonymizer") {
		return &uniqueRows{columns: []string{"last_pk", "rows_done", "done", "policy_hash"}}, nil
	}
	return &uniqueRows{columns: []string{"id", "payload"},
		values: [][]driver.Value{{int64(1), "first"}, {int64(2), "second"}}}, nil
}
func (c *uniqueConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "INSERT INTO _pxc_anonymizer.progress") {
		return nil, errors.New("unexpected unique-test write")
	}
	c.state.checkpoints++
	return driver.RowsAffected(1), nil
}
func (tx *uniqueTx) Commit() error   { tx.state.committed = true; return nil }
func (tx *uniqueTx) Rollback() error { tx.state.rolledBack = true; return nil }
func (*uniqueStmt) Close() error     { return nil }
func (*uniqueStmt) NumInput() int    { return 2 }
func (s *uniqueStmt) Exec(values []driver.Value) (driver.Result, error) {
	s.state.updates++
	if values[1] == int64(2) && (s.state.exhaust || values[0] != int64(7)) {
		return nil, &mysql.MySQLError{Number: 1062, Message: uniqueSensitive}
	}
	return driver.RowsAffected(1), nil
}
func (*uniqueStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected unique-test prepared query")
}
func (r *uniqueRows) Columns() []string { return r.columns }
func (*uniqueRows) Close() error        { return nil }
func (r *uniqueRows) Next(values []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(values, r.values[0])
	r.values = r.values[1:]
	return nil
}

func TestIdentifiersAndPrimaryKeyMutationRejected(t *testing.T) {
	for _, name := range []string{"", "a.b", "a`", "a;DROP TABLE t", strings.Repeat("x", 65)} {
		if _, err := QuoteIdentifier(name); err == nil {
			t.Fatal("unsafe identifier accepted")
		}
	}
	table := Table{Database: pagerTestDatabase, Name: pagerTestTable,
		PrimaryKey: []string{"sequence"}, Columns: []string{"SEQUENCE"}}
	if err := Validate(table); err == nil {
		t.Fatal("primary key mutation accepted")
	}
}
