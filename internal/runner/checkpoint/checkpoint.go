// Package checkpoint keeps cursors in the same database transaction as page updates.
package checkpoint

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Key struct {
	RunUID, Kind, Database, Name, PolicyHash string
}

type Progress struct {
	LastPK   []any
	RowsDone int64
	Done     bool
	Exists   bool
}

// Executor permits a session-configured connection for checkpoint schema DDL.
type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func Ensure(ctx context.Context, db Executor) error {
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS `_pxc_anonymizer`"); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _pxc_anonymizer.progress (
run_uid VARCHAR(128) NOT NULL, kind ENUM('step','table') NOT NULL,
db VARCHAR(64) NOT NULL, name VARCHAR(128) NOT NULL, last_pk JSON,
rows_done BIGINT NOT NULL DEFAULT 0, done BOOL NOT NULL DEFAULT FALSE,
policy_hash VARCHAR(80) NOT NULL, updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
PRIMARY KEY (run_uid, kind, db, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`)
	return err
}

func Drop(ctx context.Context, db Executor) error {
	_, err := db.ExecContext(ctx, "DROP SCHEMA IF EXISTS `_pxc_anonymizer`")
	return err
}

func Load(ctx context.Context, tx *sql.Tx, key Key) (Progress, error) {
	if err := validate(key); err != nil {
		return Progress{}, err
	}
	var progress Progress
	var cursor []byte
	var policyHash string
	err := tx.QueryRowContext(ctx, `SELECT last_pk, rows_done, done, policy_hash
FROM _pxc_anonymizer.progress WHERE run_uid=? AND kind=? AND db=? AND name=? FOR UPDATE`,
		key.RunUID, key.Kind, key.Database, key.Name).
		Scan(&cursor, &progress.RowsDone, &progress.Done, &policyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Progress{}, nil
	}
	if err != nil {
		return Progress{}, err
	}
	if policyHash != key.PolicyHash {
		return Progress{}, errors.New("checkpoint policy hash differs from this run")
	}
	progress.Exists = true
	if len(cursor) != 0 {
		progress.LastPK, err = decode(cursor)
	}
	return progress, err
}

func Save(ctx context.Context, tx *sql.Tx, key Key, progress Progress) error {
	if err := validate(key); err != nil {
		return err
	}
	if progress.RowsDone < 0 {
		return errors.New("checkpoint row count cannot be negative")
	}
	cursor, err := encode(progress.LastPK)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO _pxc_anonymizer.progress
(run_uid,kind,db,name,last_pk,rows_done,done,policy_hash) VALUES (?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE last_pk=VALUES(last_pk), rows_done=VALUES(rows_done),
done=VALUES(done), updated_at=CURRENT_TIMESTAMP(6)`,
		key.RunUID, key.Kind, key.Database, key.Name, cursor, progress.RowsDone, progress.Done, key.PolicyHash)
	return err
}

func validate(key Key) error {
	if key.RunUID == "" || len(key.RunUID) > 128 || key.Database == "" || len(key.Database) > 64 ||
		key.Name == "" || len(key.Name) > 128 || key.PolicyHash == "" || len(key.PolicyHash) > 80 ||
		(key.Kind != "step" && key.Kind != "table") {
		return errors.New("invalid checkpoint identity")
	}
	return nil
}

// Type tags preserve binary keys and integers beyond JSON's floating-point precision.
type value struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

func encode(key []any) ([]byte, error) {
	values := make([]value, len(key))
	for i, raw := range key {
		var item value
		switch v := raw.(type) {
		case []byte:
			item = value{"bytes", base64.StdEncoding.EncodeToString(v)}
		case string:
			item = value{"string", v}
		case int64:
			item = value{"int64", strconv.FormatInt(v, 10)}
		case uint64:
			item = value{"uint64", strconv.FormatUint(v, 10)}
		case float64:
			item = value{"float64", strconv.FormatFloat(v, 'g', -1, 64)}
		case bool:
			item = value{"bool", strconv.FormatBool(v)}
		case time.Time:
			item = value{"time", v.Format(time.RFC3339Nano)}
		default:
			return nil, fmt.Errorf("unsupported primary key type %T", raw)
		}
		values[i] = item
	}
	return json.Marshal(values)
}

func decode(data []byte) ([]any, error) {
	var values []value
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	key := make([]any, len(values))
	for i, v := range values {
		var decoded any
		var err error
		switch v.Type {
		case "bytes":
			decoded, err = base64.StdEncoding.DecodeString(v.Data)
		case "string":
			decoded = v.Data
		case "int64":
			decoded, err = strconv.ParseInt(v.Data, 10, 64)
		case "uint64":
			decoded, err = strconv.ParseUint(v.Data, 10, 64)
		case "float64":
			decoded, err = strconv.ParseFloat(v.Data, 64)
		case "bool":
			decoded, err = strconv.ParseBool(v.Data)
		case "time":
			decoded, err = time.Parse(time.RFC3339Nano, v.Data)
		default:
			return nil, errors.New("unsupported checkpoint primary key encoding")
		}
		if err != nil {
			return nil, err
		}
		key[i] = decoded
	}
	return key, nil
}
