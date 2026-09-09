// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	mysqlfixtures "github.com/ydixken/pxc-anonymizer/test/fixtures/mysql"
)

const (
	seedServiceMarker    = "`demo_users`.`seed_meta`"
	seedServiceContent   = "`demo_content`.`message_property`"
	seedServiceCommitted = "seed-demo: fixtures and marker committed\n"
	seedServiceMismatch  = "seed marker profile or scale differs; refusing to overwrite\n"
)

func TestSeedDemoMySQL(t *testing.T) {
	dsn := os.Getenv("PXC_ANONYMIZER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PXC_ANONYMIZER_TEST_MYSQL_DSN is unset; real seed-demo acceptance was not exercised")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	connection, args := seedServiceConnection(t, ctx, dsn)
	run := func(name string, test func(*testing.T)) {
		if !t.Run(name, test) {
			t.FailNow()
		}
	}
	run("wrong-credentials-are-bounded", func(t *testing.T) {
		password := make([]byte, 32)
		if _, err := rand.Read(password); err != nil {
			t.Fatal("cannot generate rejected service-test credential")
		}
		wrongFile := filepath.Join(t.TempDir(), "password")
		if err := os.WriteFile(wrongFile, []byte(hex.EncodeToString(password)), 0o600); err != nil {
			t.Fatal("cannot write private rejected credential")
		}
		wrongArgs := append(append([]string{}, args...), "--password-file", wrongFile)
		wrongCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		started := time.Now()
		seedServiceRun(t, wrongCtx, wrongArgs, 1, 1, "database authentication or access denied\n")
		if time.Since(started) >= 3*time.Second {
			t.Fatal("wrong credentials exceeded the bounded readiness wait")
		}
	})

	schema, err := mysqlfixtures.Files.ReadFile("schema.sql")
	if err != nil || len(schema) == 0 {
		t.Fatal("embedded schema fixture is unavailable")
	}
	seedServiceExec(t, ctx, connection, string(schema))
	run("unmarked-nonempty-refused", func(t *testing.T) {
		seedServiceExec(t, ctx, connection,
			"INSERT INTO "+seedServiceContent+" VALUES (777, 'owned-sentinel', 'preserve-me')")
		seedServiceRun(t, ctx, args, 1, 1, "unmarked fixture table contains data; refusing to append\n")
		seedServiceCount(t, ctx, connection,
			"SELECT COUNT(*) FROM "+seedServiceContent+
				" WHERE id=777 AND `key`='owned-sentinel' AND `value`='preserve-me'", 1)
		seedServiceCount(t, ctx, connection, "SELECT COUNT(*) FROM "+seedServiceMarker, 0)
		seedServiceExec(t, ctx, connection, "DELETE FROM "+seedServiceContent+" WHERE id=777")
		seedServiceEmpty(t, ctx, connection)
	})
	run("empty-myisam-refused", func(t *testing.T) {
		seedServiceExec(t, ctx, connection, "ALTER TABLE "+seedServiceContent+" ENGINE=MyISAM")
		seedServiceRun(t, ctx, args, 1, 1, "every fixture table must exist and use InnoDB before seeding\n")
		seedServiceEmpty(t, ctx, connection)
		seedServiceExec(t, ctx, connection, "ALTER TABLE "+seedServiceContent+" ENGINE=InnoDB")
	})
	run("mid-seed-check-failure-rolls-back", func(t *testing.T) {
		// This table is seeded after four others, so its CHECK tests cross-table rollback.
		seedServiceExec(t, ctx, connection,
			"ALTER TABLE demo_orders.commission ADD CONSTRAINT seed_service_reject CHECK (id < 1)")
		seedServiceRun(t, ctx, args, 1, 1, "fixture or marker insert failed; seed transaction rolled back\n")
		seedServiceEmpty(t, ctx, connection)
		seedServiceExec(t, ctx, connection,
			"ALTER TABLE demo_orders.commission DROP CHECK seed_service_reject")
	})
	run("scale-one-counts-and-shared-identities", func(t *testing.T) {
		seedServiceRun(t, ctx, args, 1, 0, seedServiceCommitted)
		seedServiceRows(t, ctx, connection)
	})
	run("matching-repeat-preserves-data-and-timestamp", func(t *testing.T) {
		checksums, seededAt := seedServiceSnapshot(t, ctx, connection)
		seedServiceRun(t, ctx, args, 1, 0, "seed-demo: matching marker, data unchanged\n")
		seedServiceUnchanged(t, ctx, connection, checksums, seededAt)
	})
	run("scale-mismatch-refused", func(t *testing.T) {
		checksums, seededAt := seedServiceSnapshot(t, ctx, connection)
		seedServiceRun(t, ctx, args, 2, 1, seedServiceMismatch)
		seedServiceUnchanged(t, ctx, connection, checksums, seededAt)
	})
	run("profile-mismatch-refused", func(t *testing.T) {
		seedServiceExec(t, ctx, connection,
			"UPDATE "+seedServiceMarker+" SET profile='other-profile' WHERE id=1")
		checksums, seededAt := seedServiceSnapshot(t, ctx, connection)
		seedServiceRun(t, ctx, args, 1, 1, seedServiceMismatch)
		seedServiceUnchanged(t, ctx, connection, checksums, seededAt)
	})
	t.Log("real MySQL seed-demo: refusals, rollback, scale-one identities and marker idempotency verified")
}

func seedServiceConnection(t *testing.T, ctx context.Context, dsn string) (*sql.Conn, []string) {
	t.Helper()
	configuration, err := mysql.ParseDSN(dsn)
	if err != nil || configuration.Net != "tcp" || configuration.Passwd == "" {
		t.Fatal("seed-demo service test requires a valid TCP MySQL DSN with a password")
	}
	host, port, err := net.SplitHostPort(configuration.Addr)
	if err != nil || (configuration.TLSConfig != "" && configuration.TLSConfig != seedTestTLSConfigDisabled) {
		t.Fatal("seed-demo service test requires the isolated MySQL service without TLS")
	}
	configuration.DBName = ""
	configuration.MultiStatements, configuration.ParseTime = true, true
	configuration.Timeout = 2 * time.Second
	configuration.Logger = log.New(io.Discard, "", 0)
	connector, err := mysql.NewConnector(configuration)
	if err != nil {
		t.Fatal("cannot initialize seed-demo service connection")
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("configured MySQL seed-demo service is unreachable")
	}
	var existing int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE LEFT(SCHEMA_NAME, 5)='demo_'",
	).Scan(&existing); err != nil || existing != 0 {
		t.Fatal("refusing seed-demo service test: demo_ schemas already exist or cannot be inspected")
	}
	// Cleanup is registered only after each exact CREATE succeeds; preexisting schemas are never adopted.
	for _, schema := range []string{"demo_users", "demo_orders", "demo_content"} {
		if _, err := db.ExecContext(ctx, "CREATE DATABASE `"+schema+"`"); err != nil {
			t.Fatal("cannot create owned seed-demo schema")
		}
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if _, err := db.ExecContext(cleanupCtx, "DROP DATABASE `"+schema+"`"); err != nil {
				t.Error("cannot remove exact owned seed-demo schema")
				return
			}
			var remaining int
			if err := db.QueryRowContext(cleanupCtx,
				"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", schema,
			).Scan(&remaining); err != nil || remaining != 0 {
				t.Error("owned seed-demo schema cleanup could not be confirmed")
				return
			}
			t.Logf("removed exact owned service schema %s", schema)
		})
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal("cannot retain seed-demo service connection")
	}
	t.Cleanup(func() { _ = connection.Close() })
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte(configuration.Passwd), 0o600); err != nil {
		t.Fatal("cannot write private seed-demo password file")
	}
	return connection, []string{
		"--host", host, "--port", port, "--user", configuration.User,
		"--password-file", passwordFile, "--wait", "2s", "--tls-mode", seedTestTLSDisabled,
	}
}

func seedServiceRun(t *testing.T, ctx context.Context, args []string, scale, exit int, message string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	argv := append(append([]string{}, args...), "--scale", strconv.Itoa(scale))
	actual := runSeedDemo(ctx, argv, &stdout, &stderr)
	if actual != exit {
		t.Fatalf("seed-demo exit code = %d, want %d; captured output withheld", actual, exit)
	}
	if (exit == 0 && (stdout.String() != message || stderr.Len() != 0)) ||
		(exit != 0 && (stderr.String() != message || stdout.Len() != 0)) {
		t.Fatal("seed-demo returned unexpected output; captured output withheld")
	}
}

func seedServiceExec(t *testing.T, ctx context.Context, connection *sql.Conn, query string) {
	t.Helper()
	if _, err := connection.ExecContext(ctx, query); err != nil {
		t.Fatal("owned seed-demo fixture operation failed")
	}
}

func seedServiceCount(t *testing.T, ctx context.Context, connection *sql.Conn, query string, want int) {
	t.Helper()
	var actual int
	if err := connection.QueryRowContext(ctx, query).Scan(&actual); err != nil {
		t.Fatal("owned seed-demo count query failed")
	}
	if actual != want {
		t.Fatalf("owned fixture row count = %d, want %d", actual, want)
	}
}

func seedServiceEmpty(t *testing.T, ctx context.Context, connection *sql.Conn) {
	t.Helper()
	if len(seedTables) != 10 {
		t.Fatal("expected all ten fixture tables in rollback check")
	}
	for _, table := range seedTables {
		seedServiceCount(t, ctx, connection, "SELECT COUNT(*) FROM `"+table[0]+"`.`"+table[1]+"`", 0)
	}
}

func seedServiceRows(t *testing.T, ctx context.Context, connection *sql.Conn) {
	t.Helper()
	for table, count := range map[string]int{
		"`demo_users`.`user`": 5000, "demo_users.organisation": 1000, "demo_users.address": 8000,
		"demo_users.email_blacklist": 300, seedServiceMarker: 1,
		"demo_orders.commission": 10000, "demo_orders.commission_identity": 10000,
		"demo_orders.commission_details_message": 3000, "demo_orders.audit_log": 2, seedServiceContent: 50,
	} {
		seedServiceCount(t, ctx, connection, "SELECT COUNT(*) FROM "+table, count)
	}
	seedServiceCount(t, ctx, connection,
		"SELECT COUNT(*) FROM demo_orders.commission c JOIN demo_users.user u ON u.id=c.user_id "+
			"JOIN demo_orders.commission_identity i ON i.commission_id=c.id "+
			"WHERE c.email=u.email AND c.given_name=u.given_name "+
			"AND c.family_name=u.family_name AND i.email=c.email", 10000)
	seedServiceCount(t, ctx, connection,
		"SELECT COUNT(*) FROM demo_users.address a "+
			"JOIN demo_users.user u ON u.id=a.user_id WHERE a.phone=u.phone", 8000)
	seedServiceCount(t, ctx, connection,
		"SELECT COUNT(*) FROM demo_users.email_blacklist b "+
			"JOIN demo_users.user u ON u.id=b.id WHERE b.email=u.email", 300)
	seedServiceCount(t, ctx, connection,
		"SELECT COUNT(*) FROM demo_orders.audit_log WHERE user_id=1 AND event_id IN (1,2)", 2)
	seedServiceCount(t, ctx, connection,
		"SELECT COUNT(*) FROM "+seedServiceMarker+" WHERE id=1 AND profile='demo-v1' AND scale=1", 1)
}

func seedServiceSnapshot(t *testing.T, ctx context.Context, connection *sql.Conn) (map[string]uint64, time.Time) {
	t.Helper()
	checksums := make(map[string]uint64, len(seedTables))
	for _, table := range seedTables {
		var name string
		var checksum uint64
		if err := connection.QueryRowContext(ctx,
			"CHECKSUM TABLE `"+table[0]+"`.`"+table[1]+"`",
		).Scan(&name, &checksum); err != nil {
			t.Fatal("owned seed-demo checksum query failed")
		}
		checksums[name] = checksum
	}
	var seededAt time.Time
	if err := connection.QueryRowContext(ctx,
		"SELECT seeded_at FROM "+seedServiceMarker+" WHERE id=1",
	).Scan(&seededAt); err != nil || seededAt.IsZero() {
		t.Fatal("seed marker timestamp is missing or invalid")
	}
	return checksums, seededAt
}

func seedServiceUnchanged(t *testing.T, ctx context.Context, connection *sql.Conn,
	checksums map[string]uint64, seededAt time.Time,
) {
	t.Helper()
	after, timestamp := seedServiceSnapshot(t, ctx, connection)
	if !maps.Equal(checksums, after) || !seededAt.Equal(timestamp) {
		t.Fatal("seed-demo changed fixture checksums or marker timestamp")
	}
}
