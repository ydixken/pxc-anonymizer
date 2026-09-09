// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/ydixken/pxc-anonymizer/internal/runner/engine"
	mysqlfixtures "github.com/ydixken/pxc-anonymizer/test/fixtures/mysql"
)

const (
	seedProfile      = "demo-v1"
	seedUsersSchema  = "demo_users"
	seedOrdersSchema = "demo_orders"
)

var seedTables = [][2]string{
	{seedUsersSchema, "user"}, {seedUsersSchema, "organisation"},
	{seedUsersSchema, "address"}, {seedUsersSchema, "email_blacklist"}, {seedUsersSchema, "seed_meta"},
	{seedOrdersSchema, "commission"}, {seedOrdersSchema, "commission_identity"},
	{seedOrdersSchema, "commission_details_message"}, {seedOrdersSchema, "audit_log"},
	{"demo_content", "message_property"},
}

type seedFlags struct {
	host, user, passwordFile, tlsMode, tlsCAFile string
	port, scale                                  int
	wait                                         time.Duration
}

func seedFlagSet(settings *seedFlags) *flag.FlagSet {
	flags := flag.NewFlagSet("seed-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&settings.host, "host", "", "MySQL server hostname (required)")
	flags.IntVar(&settings.port, "port", 3306, "MySQL server port")
	flags.StringVar(&settings.user, "user", "root", "Database username")
	flags.StringVar(&settings.passwordFile, "password-file", "/etc/pxc-anonymizer/creds/root", "Raw password file")
	flags.DurationVar(&settings.wait, "wait", 30*time.Minute, "Maximum wait for MySQL readiness")
	flags.IntVar(&settings.scale, "scale", 1, "Integer fixture scale (1 through 10)")
	flags.StringVar(&settings.tlsMode, "tls-mode", "disabled", "Database TLS mode: disabled or verify-full")
	flags.StringVar(&settings.tlsCAFile, "tls-ca-file", "", "Optional custom database CA file")
	return flags
}

func runSeedDemo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	settings := seedFlags{}
	flags := seedFlagSet(&settings)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			return 0
		}
		_, _ = fmt.Fprintln(stderr, "invalid seed-demo flags")
		return 1
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "seed-demo accepts flags only")
		return 1
	}
	configuration, err := prepareSeed(&settings)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	connector, err := mysql.NewConnector(configuration)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid database connection configuration")
		return 1
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	connection, err := waitSeedConnection(ctx, db, settings.wait)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = connection.Close() }()
	unchanged, err := applySeed(ctx, connection, settings.scale)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if unchanged {
		_, _ = fmt.Fprintln(stdout, "seed-demo: matching marker, data unchanged")
	} else {
		_, _ = fmt.Fprintln(stdout, "seed-demo: fixtures and marker committed")
	}
	return 0
}

func prepareSeed(settings *seedFlags) (*mysql.Config, error) {
	if settings.host == "" || strings.ContainsAny(settings.host, "\r\n /\\") ||
		settings.port < 1 || settings.port > 65535 || settings.user == "" {
		return nil, errors.New("invalid database address or username")
	}
	if settings.scale < 1 || settings.scale > 10 || settings.wait <= 0 {
		return nil, errors.New("scale must be 1 through 10 and wait must be positive")
	}
	password, err := engine.ReadFile(settings.passwordFile, 1<<20)
	if err != nil || len(password) == 0 {
		return nil, errors.New("password file must contain a nonempty value")
	}
	configuration := mysql.NewConfig()
	configuration.Net = "tcp"
	configuration.Addr = net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
	configuration.User, configuration.Passwd = settings.user, string(password)
	configuration.MultiStatements = true
	configuration.ParseTime = true
	configuration.Timeout = 10 * time.Second
	configuration.Logger = log.New(io.Discard, "", 0)
	if err := configureAnonymizeTLS(configuration, &anonymizeFlags{
		host: settings.host, tlsMode: settings.tlsMode, tlsCAFile: settings.tlsCAFile,
	}); err != nil {
		return nil, err
	}
	return configuration, nil
}

func waitSeedConnection(ctx context.Context, db *sql.DB, wait time.Duration) (*sql.Conn, error) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		connection, err := db.Conn(waitCtx)
		if err == nil {
			err = connection.PingContext(waitCtx)
			if err == nil {
				return connection, nil
			}
			_ = connection.Close()
		}
		var serverError *mysql.MySQLError
		if errors.As(err, &serverError) && (serverError.Number == 1044 || serverError.Number == 1045) {
			return nil, errors.New("database authentication or access denied")
		}
		select {
		case <-waitCtx.Done():
			return nil, errors.New("MySQL readiness wait expired or was canceled")
		case <-ticker.C:
		}
	}
}

func applySeed(ctx context.Context, connection *sql.Conn, scale int) (bool, error) {
	var profile string
	var recordedScale int
	err := connection.QueryRowContext(ctx,
		"SELECT profile, scale FROM demo_users.seed_meta WHERE id = 1").Scan(&profile, &recordedScale)
	if err == nil {
		if profile != seedProfile || recordedScale != scale {
			return false, errors.New("seed marker profile or scale differs; refusing to overwrite")
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) && !seedTableAbsent(err) {
		return false, errors.New("seed marker cannot be read")
	}
	if err := seedTablesEmpty(ctx, connection); err != nil {
		return false, err
	}
	files := make([]string, 0, 3)
	for _, name := range []string{"schema.sql", "seed.sql", "finish.sql"} {
		data, err := mysqlfixtures.Files.ReadFile(name)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false, errors.New("embedded seed fixture is missing or empty")
		}
		files = append(files, string(data))
	}
	// MySQL DDL commits implicitly, so it must precede the seed transaction.
	if _, err := connection.ExecContext(ctx, files[0]); err != nil {
		return false, errors.New("fixture schema could not be created")
	}
	if err := seedTablesTransactional(ctx, connection); err != nil {
		return false, err
	}
	if _, err := connection.ExecContext(ctx, "SET SESSION cte_max_recursion_depth = 1000000"); err != nil {
		return false, errors.New("fixture recursion limit could not be configured")
	}
	if _, err := connection.ExecContext(ctx,
		"SET @pxc_seed_scale = ?, @pxc_seed_profile = ?", scale, seedProfile); err != nil {
		return false, errors.New("fixture session variables could not be configured")
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("seed transaction could not be started")
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range files[1:] {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return false, errors.New("fixture or marker insert failed; seed transaction rolled back")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("seed commit could not be confirmed; inspect the marker before retrying")
	}
	return false, nil
}

func seedTablesEmpty(ctx context.Context, connection *sql.Conn) error {
	for _, table := range seedTables {
		var nonempty bool
		err := connection.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM `"+table[0]+"`.`"+table[1]+"` LIMIT 1)").Scan(&nonempty)
		if seedTableAbsent(err) {
			continue
		}
		if err != nil {
			return errors.New("fixture table cannot be inspected")
		}
		if nonempty {
			return errors.New("unmarked fixture table contains data; refusing to append")
		}
	}
	return nil
}

func seedTablesTransactional(ctx context.Context, connection *sql.Conn) error {
	for _, table := range seedTables {
		var storageEngine sql.NullString
		err := connection.QueryRowContext(ctx,
			"SELECT ENGINE FROM information_schema.tables WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?",
			table[0], table[1]).Scan(&storageEngine)
		if err != nil || !storageEngine.Valid || !strings.EqualFold(storageEngine.String, "InnoDB") {
			return errors.New("every fixture table must exist and use InnoDB before seeding")
		}
	}
	return nil
}

func seedTableAbsent(err error) bool {
	var serverError *mysql.MySQLError
	return errors.As(err, &serverError) && (serverError.Number == 1049 || serverError.Number == 1146)
}
