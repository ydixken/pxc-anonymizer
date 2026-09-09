package pager_test

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/ydixken/pxc-anonymizer/internal/runner/checkpoint"
	"github.com/ydixken/pxc-anonymizer/internal/runner/pager"
	"github.com/ydixken/pxc-anonymizer/internal/runner/sqlstep"
)

const (
	testDSN       = "PXC_ANONYMIZER_TEST_MYSQL_DSN"
	childFlag     = "PXC_ANONYMIZER_PAGER_CHILD"
	childDatabase = "PXC_ANONYMIZER_PAGER_DATABASE"
	checkpointUID = "PXC_ANONYMIZER_PAGER_UID"
	testHash      = "sha256:pager-test"
	compositeName = "composite"
)

func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(testDSN)
	if dsn == "" {
		t.Skip("real MySQL acceptance not run: " + testDSN + " is unset")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid MySQL test configuration")
	}
	config.MultiStatements = true
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal("invalid MySQL connector configuration")
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("MySQL test database is unavailable")
	}
	return db
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func compositeTable(database string) pager.Table {
	return pager.Table{Database: database, Name: compositeName, PrimaryKey: []string{"tenant", "sequence"},
		Columns: []string{"value", "hits"}, PageSize: 2}
}

func transform(_ context.Context, row pager.Row) ([]any, error) {
	return []any{"!" + string(row.Values[0].([]byte)), row.Values[1].(int64) + 1}, nil
}

func TestPagerKilledProcess(t *testing.T) {
	if os.Getenv(childFlag) != "1" {
		t.Skip("subprocess helper")
	}
	db := openMySQL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	database := os.Getenv(childDatabase)
	key := checkpoint.Key{RunUID: os.Getenv(checkpointUID), Kind: "table", Database: database,
		Name: compositeName, PolicyHash: testHash}
	calls := 0
	_, err = pager.ProcessPage(ctx, conn, compositeTable(database), key, func(ctx context.Context, row pager.Row) ([]any, error) {
		calls++
		if calls == 2 {
			if _, err := fmt.Fprintln(os.Stdout, "page-open"); err != nil {
				return nil, err
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return transform(ctx, row)
	})
	t.Fatalf("parent failed to kill subprocess: %v", err)
}

func TestPagerMySQL(t *testing.T) {
	db := openMySQL(t)
	database := "pxc_pager_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	uid := database
	execSQL(t, db, "CREATE DATABASE `"+database+"`")
	if err := checkpoint.Ensure(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DELETE FROM _pxc_anonymizer.progress WHERE run_uid=?", uid); err != nil {
			t.Error(err)
		}
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+database+"`"); err != nil {
			t.Error(err)
		}
	})
	execSQL(t, db, "CREATE TABLE `"+database+"`.composite (tenant VARBINARY(16), sequence BIGINT,"+
		" value VARCHAR(32), hits INT NOT NULL, PRIMARY KEY(tenant,sequence)) ENGINE=InnoDB")
	for i := 4; i >= 0; i-- {
		execSQL(t, db, "INSERT INTO `"+database+"`.composite VALUES (?,?,?,0)",
			[]byte{0, byte(i / 2)}, int64(9007199254740993)+int64(i), "original")
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	key := checkpoint.Key{RunUID: uid, Kind: "table", Database: database, Name: compositeName, PolicyHash: testHash}
	progress, err := pager.ProcessPage(t.Context(), conn, compositeTable(database), key, transform)
	if err != nil || progress.RowsDone != 2 || progress.Done {
		t.Fatalf("first page: %+v %v", progress, err)
	}
	t.Run("kill-uncommitted-page", func(t *testing.T) { killPage(t, database, uid) })
	var updated int
	if err := conn.QueryRowContext(t.Context(), "SELECT SUM(hits) FROM `"+database+"`.composite").Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("killed page committed rows without checkpoint: %d", updated)
	}
	for !progress.Done {
		progress, err = pager.ProcessPage(t.Context(), conn, compositeTable(database), key, transform)
		if err != nil {
			t.Fatal(err)
		}
	}
	if progress.RowsDone != 5 {
		t.Fatalf("resume processed %d rows", progress.RowsDone)
	}
	var incorrect int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM `"+database+
		"`.composite WHERE hits <> 1 OR value <> '!original'").Scan(&incorrect); err != nil {
		t.Fatal(err)
	}
	if incorrect != 0 {
		t.Fatal("resume double-processed or skipped a row")
	}
	t.Run("sql-results-and-uncertain-ddl", func(t *testing.T) { sqlSteps(t, conn, database, uid) })
}

func killPage(t *testing.T, database, uid string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPagerKilledProcess$")
	cmd.Env = append(os.Environ(), childFlag+"=1", childDatabase+"="+database, checkpointUID+"="+uid)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "page-open\n" {
		_ = cmd.Wait()
		t.Fatal("child did not open the uncommitted page")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child unexpectedly exited successfully")
	}
}

func sqlSteps(t *testing.T, conn *sql.Conn, database, uid string) {
	t.Helper()
	body := "SELECT 'a;b'; INSERT INTO composite VALUES ('step', 1, 'semi;colon', 0); SELECT 2"
	key := checkpoint.Key{RunUID: uid, Kind: "step", Database: database, Name: "multi", PolicyHash: testHash}
	if executed, err := sqlstep.Run(t.Context(), conn, database, body, key, true); err != nil || executed {
		t.Fatalf("dry-run step: %t %v", executed, err)
	}
	if executed, err := sqlstep.Run(t.Context(), conn, database, body, key, false); err != nil || !executed {
		t.Fatalf("multi-result step: %t %v", executed, err)
	}
	if executed, err := sqlstep.Run(t.Context(), conn, database, body, key, false); err != nil || executed {
		t.Fatalf("completed step replay: %t %v", executed, err)
	}
	key.Name = "partial"
	body = "INSERT INTO composite VALUES ('partial',1,'once',0); SELECT * FROM missing_table"
	if _, err := sqlstep.Run(t.Context(), conn, database, body, key, false); err == nil {
		t.Fatal("SQL failure was ignored")
	}
	if _, err := sqlstep.Run(t.Context(), conn, database, body, key, false); !errors.Is(err, sqlstep.ErrUncertainOutcome) {
		t.Fatalf("partial SQL replay was not refused: %v", err)
	}
}
