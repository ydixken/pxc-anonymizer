// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/ydixken/pxc-anonymizer/internal/runner/engine"
	"github.com/ydixken/pxc-anonymizer/internal/runner/report"
)

type anonymizeFlags struct {
	policy, host, user, passwordFile, seedFile string
	reference, logFormat, terminationFile      string
	tlsMode, tlsCAFile                         string
	port, attempt                              int
	options                                    engine.Options
}

func anonymizeFlagSet(settings *anonymizeFlags) *flag.FlagSet {
	flags := flag.NewFlagSet("anonymize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&settings.policy, "policy", "/etc/pxc-anonymizer/policy/policy.json", "Canonical policy snapshot file")
	flags.StringVar(&settings.host, "host", "", "Temporary MySQL server hostname")
	flags.IntVar(&settings.port, "port", 3306, "Temporary MySQL server port")
	flags.StringVar(&settings.user, "user", "root", "Database username")
	flags.StringVar(&settings.passwordFile, "password-file", "/etc/pxc-anonymizer/creds/root", "Projected password file")
	flags.StringVar(&settings.seedFile, "seed-file", "/etc/pxc-anonymizer/seed/seed", "Projected raw seed file")
	flags.StringVar(&settings.options.RunUID, "run-uid", "", "Owning Run UID")
	flags.IntVar(&settings.attempt, "attempt", 1, "Run attempt number")
	flags.IntVar(&settings.options.Workers, "workers", 4, "Concurrent table workers")
	flags.IntVar(&settings.options.PageSize, "page-size", 5000, "Default keyset page size")
	flags.BoolVar(&settings.options.DisableBinlog, "disable-binlog", true, "Disable binary logging on write connections")
	flags.BoolVar(&settings.options.DryRun, "dry-run", false, "Validate policy and database metadata without writes")
	flags.DurationVar(&settings.options.ProgressInterval, "progress-interval", 10*time.Second,
		"Minimum periodic progress interval")
	flags.StringVar(&settings.logFormat, "log-format", "json", "Log format (json)")
	flags.StringVar(&settings.reference, "reference-time", "", "Owning Run creation timestamp in RFC3339")
	flags.StringVar(&settings.options.StepsDir, "steps-dir", "/etc/pxc-anonymizer/steps", "Projected SQL files directory")
	flags.StringVar(&settings.options.ConstantsDir, "constants-dir", "/etc/pxc-anonymizer/constants",
		"Projected constant files directory")
	flags.StringVar(&settings.terminationFile, "termination-file", "/dev/termination-log", "Termination report file")
	flags.StringVar(&settings.tlsMode, "tls-mode", "verify-full", "Database TLS mode: verify-full or disabled")
	flags.StringVar(&settings.tlsCAFile, "tls-ca-file", "", "Optional custom database CA file")
	return flags
}

func runAnonymize(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	settings := anonymizeFlags{}
	flags := anonymizeFlagSet(&settings)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			return 0
		}
		return finishAnonymize(report.Report{
			Result: report.ResultError, Class: report.Policy, Message: "invalid anonymize flags",
		}, settings.terminationFile, stdout, stderr)
	}
	if flags.NArg() != 0 {
		return finishAnonymize(report.Report{
			Result: report.ResultError, Class: report.Policy, Message: "anonymize accepts flags only",
		}, settings.terminationFile, stdout, stderr)
	}
	settings.options.Progress = stdout
	configuration, err := prepareAnonymize(&settings)
	if err != nil {
		class, message := report.Classify(err)
		return finishAnonymize(report.Report{
			Result: report.ResultError, Class: class, Message: message,
		}, settings.terminationFile, stdout, stderr)
	}
	connector, err := mysql.NewConnector(configuration)
	if err != nil {
		return finishAnonymize(report.Report{
			Result: report.ResultError, Class: report.Policy, Message: "invalid database connection configuration",
		}, settings.terminationFile, stdout, stderr)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(settings.options.Workers + 1)
	db.SetMaxIdleConns(settings.options.Workers + 1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return finishAnonymize(engine.Run(ctx, db, settings.options), settings.terminationFile, stdout, stderr)
}

func prepareAnonymize(settings *anonymizeFlags) (*mysql.Config, error) {
	if settings.host == "" || strings.ContainsAny(settings.host, "\r\n /\\") ||
		settings.port < 1 || settings.port > 65535 || settings.user == "" ||
		settings.attempt < 1 || settings.logFormat != "json" {
		return nil, report.Fail(report.Policy, "invalid database address, attempt or log format")
	}
	if settings.options.Workers < 1 || settings.options.Workers > 32 ||
		settings.options.PageSize < 1 || settings.options.PageSize > 50000 || settings.options.ProgressInterval <= 0 {
		return nil, report.Fail(report.Policy, "invalid worker, page size or progress interval")
	}
	reference, err := time.Parse(time.RFC3339, settings.reference)
	if err != nil {
		return nil, report.Fail(report.Policy, "reference-time must contain the immutable Run creation timestamp")
	}
	settings.options.ReferenceTime = reference
	settings.options.PolicyJSON, err = engine.ReadFile(settings.policy, 1<<20)
	if err != nil {
		return nil, report.Fail(report.Policy, "policy snapshot file cannot be read")
	}
	settings.options.Seed, err = engine.ReadFile(settings.seedFile, 1<<20)
	if err != nil || len(settings.options.Seed) < 32 {
		return nil, report.Fail(report.Policy, "seed file must contain at least 32 raw bytes")
	}
	password, err := engine.ReadFile(settings.passwordFile, 1<<20)
	if err != nil || len(password) == 0 {
		return nil, report.Fail(report.Policy, "password file must contain a nonempty value")
	}
	configuration := mysql.NewConfig()
	configuration.Net = "tcp"
	configuration.Addr = net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
	configuration.User, configuration.Passwd = settings.user, string(password)
	configuration.MultiStatements = true
	configuration.ParseTime = true
	configuration.Timeout = 10 * time.Second
	configuration.Logger = log.New(io.Discard, "", 0)
	if err := configureAnonymizeTLS(configuration, settings); err != nil {
		return nil, err
	}
	return configuration, nil
}

func configureAnonymizeTLS(configuration *mysql.Config, settings *anonymizeFlags) error {
	switch settings.tlsMode {
	case "disabled":
		if settings.tlsCAFile != "" {
			return report.Fail(report.Policy, "tls-ca-file requires verify-full TLS")
		}
		configuration.TLSConfig = "false"
	case "verify-full":
		configuration.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings.host}
		if settings.tlsCAFile == "" {
			return nil
		}
		data, err := engine.ReadFile(settings.tlsCAFile, 1<<20)
		if err != nil {
			return report.Fail(report.Policy, "database CA file cannot be read")
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return report.Fail(report.Policy, "database CA file contains no certificates")
		}
		configuration.TLS.RootCAs = pool
	default:
		return report.Fail(report.Policy, "tls-mode must be verify-full or disabled")
	}
	return nil
}

func finishAnonymize(result report.Report, path string, stdout, stderr io.Writer) int {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		err = errors.Join(result.Write(file), file.Close())
	}
	_ = result.Write(stdout)
	if err != nil {
		_, _ = io.WriteString(stderr, "termination report could not be written\n")
		return 1
	}
	return result.ExitCode()
}
