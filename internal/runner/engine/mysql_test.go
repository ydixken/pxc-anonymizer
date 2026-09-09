// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

func TestEngineMySQLDryRunAndExecution(t *testing.T) {
	dsn := os.Getenv("PXC_ANONYMIZER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PXC_ANONYMIZER_TEST_MYSQL_DSN is unset; real MySQL acceptance was not exercised")
	}
	configuration, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid MySQL acceptance configuration")
	}
	configuration.MultiStatements = true
	connector, err := mysql.NewConnector(configuration)
	if err != nil {
		t.Fatal("cannot initialize MySQL acceptance connection")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("configured MySQL acceptance server is unreachable")
	}
	assertNoCheckpoint(t, ctx, db)
	database := fmt.Sprintf("engine_test_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
		t.Fatal("cannot create isolated MySQL fixture")
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "DROP DATABASE `"+database+"`")
	}()
	for _, statement := range []string{
		"CREATE TABLE `" + database + "`.people (id BIGINT PRIMARY KEY,payload VARCHAR(128) NULL)",
		"INSERT INTO `" + database + "`.people VALUES (1,'first'),(2,'second'),(3,NULL)",
		"CREATE TABLE `" + database + "`.missing_key (payload VARCHAR(128))",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal("cannot initialize isolated MySQL fixture")
		}
	}
	options := testOptions(t, api.ColumnRule{Name: testPayload, Strategy: api.StrategyHash})
	var spec api.AnonymizationPolicySpec
	if err := json.Unmarshal(options.PolicyJSON, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Databases[0].Name = database
	options.PolicyJSON, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	options.DryRun, options.PageSize = true, 2
	before := tableChecksum(t, ctx, db, database)
	result := Run(ctx, db, options)
	if result.ExitCode() != 0 || result.TablesTotal != 1 {
		t.Fatalf("real dry-run failed: %+v", result)
	}
	if after := tableChecksum(t, ctx, db, database); before != after {
		t.Fatal("real dry-run changed CHECKSUM TABLE")
	}
	assertNoCheckpoint(t, ctx, db)
	spec.Databases[0].Tables[0].Name = "missing_key"
	options.PolicyJSON, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	options.DryRun = false
	if result := Run(ctx, db, options); result.Class != report.Schema || result.ExitCode() != 11 {
		t.Fatal("real PK-less table was not rejected during preflight")
	}
	assertNoCheckpoint(t, ctx, db)
	spec.Databases[0].Tables[0].Name = "people"
	options.PolicyJSON, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	result = Run(ctx, db, options)
	if result.ExitCode() != 0 || result.TablesDone != 1 || result.RowsDone != 3 {
		t.Fatalf("real paged execution failed: %+v", result)
	}
	if after := tableChecksum(t, ctx, db, database); after == before {
		t.Fatal("real execution did not change fixture data")
	}
	assertNoCheckpoint(t, ctx, db)
	t.Log("real MySQL: dry-run checksum unchanged; no checkpoint schema; PK-less rejected before writes; 3 rows anonymized and checkpoint schema removed")
}

func assertNoCheckpoint(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='_pxc_anonymizer'").Scan(&count); err != nil || count != 0 {
		t.Fatal("checkpoint schema unexpectedly exists or could not be checked")
	}
}

func tableChecksum(t *testing.T, ctx context.Context, db *sql.DB, database string) uint64 {
	t.Helper()
	var name string
	var checksum uint64
	if err := db.QueryRowContext(ctx, "CHECKSUM TABLE `"+database+"`.people").Scan(&name, &checksum); err != nil {
		t.Fatal("CHECKSUM TABLE failed")
	}
	return checksum
}
