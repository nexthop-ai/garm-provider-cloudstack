// SPDX-License-Identifier: Apache-2.0
// Copyright 2025-present, Nexthop Systems, Inc.
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

// Package store persists small amounts of provider state across invocations.
//
// GARM runs the provider as a fresh process for every operation, so anything
// learned at runtime (how long CloudStack jobs take, later resolved UUIDs)
// has to live on disk to be of any use. The state lives in a dedicated
// SQLite database: many provider processes run concurrently and each holds
// the write lock for milliseconds, so they simply take turns.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
)

// schemaVersion is bumped whenever the schema changes; migrations are
// applied in order in Open.
const schemaVersion = 1

// Store is a handle on the provider state database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the state database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	// Same pragmas GARM uses for its own database. WAL lets readers proceed
	// while another process writes; the busy timeout makes concurrent
	// writers wait for each other instead of failing.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate&_synchronous=NORMAL", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening state db: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close() //nolint:errcheck // the migration error is the one worth reporting
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("state db schema version %d is newer than supported %d", version, schemaVersion)
	}
	if version < 1 {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE job_samples (
	id          INTEGER PRIMARY KEY,
	op          TEXT    NOT NULL,
	key         TEXT    NOT NULL,
	duration_ms INTEGER NOT NULL,
	recorded_at INTEGER NOT NULL
);
CREATE INDEX job_samples_op_key ON job_samples (op, key, id);
INSERT INTO schema_version (version) VALUES (1);
`); err != nil {
			return fmt.Errorf("applying schema v1: %w", err)
		}
	}
	return tx.Commit()
}

// RecordSample stores how long a CloudStack job of the given operation and
// key took, keeping only the most recent keep samples for that op/key.
func (s *Store) RecordSample(ctx context.Context, op, key string, d time.Duration, keep int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO job_samples (op, key, duration_ms, recorded_at) VALUES (?, ?, ?, ?)`,
		op, key, d.Milliseconds(), time.Now().Unix()); err != nil {
		return fmt.Errorf("inserting sample: %w", err)
	}
	if keep > 0 {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM job_samples WHERE op = ? AND key = ? AND id NOT IN (
	SELECT id FROM job_samples WHERE op = ? AND key = ? ORDER BY id DESC LIMIT ?
)`, op, key, op, key, keep); err != nil {
			return fmt.Errorf("pruning samples: %w", err)
		}
	}
	return tx.Commit()
}

// Samples returns the most recent samples for op/key (newest first), at most
// limit of them. An empty key returns samples across all keys of the op.
func (s *Store) Samples(ctx context.Context, op, key string, limit int) ([]time.Duration, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if key == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT duration_ms FROM job_samples WHERE op = ? ORDER BY id DESC LIMIT ?`, op, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT duration_ms FROM job_samples WHERE op = ? AND key = ? ORDER BY id DESC LIMIT ?`, op, key, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("querying samples: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only cursor

	var out []time.Duration
	for rows.Next() {
		var ms int64
		if err := rows.Scan(&ms); err != nil {
			return nil, fmt.Errorf("scanning sample: %w", err)
		}
		out = append(out, time.Duration(ms)*time.Millisecond)
	}
	return out, rows.Err()
}
