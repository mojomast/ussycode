// Package db provides SQLite database access with WAL mode,
// split reader/writer connection pools, and embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB holds the split reader/writer connection pools.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	mu     sync.Mutex // serialize writes at the application level
}

// Open creates a new DB with separate reader and writer pools.
// The database file is created if it doesn't exist.
func Open(path string) (*DB, error) {
	writer, err := openConn(path, false)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}

	reader, err := openConn(path, true)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}

	db := &DB{writer: writer, reader: reader}

	// Run pragmas on writer connection
	if err := db.pragmas(writer); err != nil {
		db.Close()
		return nil, fmt.Errorf("writer pragmas: %w", err)
	}

	// Run pragmas on reader connection
	if err := db.pragmas(reader); err != nil {
		db.Close()
		return nil, fmt.Errorf("reader pragmas: %w", err)
	}

	return db, nil
}

func openConn(path string, readonly bool) (*sql.DB, error) {
	// Use _txlock=immediate for the writer to prevent SQLITE_BUSY on
	// concurrent BEGIN; for the reader we use the same base DSN since
	// read-only semantics are enforced at the transaction level
	// (sql.TxOptions{ReadOnly: true}). Avoid SQLite URI mode=ro which
	// some drivers handle inconsistently and can cause WAL visibility issues.
	dsn := path + "?_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if readonly {
		db.SetMaxOpenConns(4)
	} else {
		db.SetMaxOpenConns(1)
	}

	// Verify the connection works
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func (d *DB) pragmas(conn *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = wal",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = normal",
		"PRAGMA cache_size = -64000",
		"PRAGMA foreign_keys = on",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// Migrate runs all pending database migrations.
func (d *DB) Migrate(ctx context.Context) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, d.writer, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// WriteTx executes fn inside a write transaction. Only one write
// transaction runs at a time (serialized by application mutex).
func (d *DB) WriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}

	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// ReadTx executes fn inside a read-only transaction.
// Multiple read transactions can run concurrently.
func (d *DB) ReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}

	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// Writer returns the raw writer *sql.DB for cases where you need
// direct access (e.g., single statements that don't need a transaction).
func (d *DB) Writer() *sql.DB {
	return d.writer
}

// Reader returns the raw reader *sql.DB.
func (d *DB) Reader() *sql.DB {
	return d.reader
}

// Close closes both reader and writer connection pools.
func (d *DB) Close() error {
	var errs []error
	if d.reader != nil {
		if err := d.reader.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close reader: %w", err))
		}
	}
	if d.writer != nil {
		if err := d.writer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close writer: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close db: %v", errs)
	}
	return nil
}
