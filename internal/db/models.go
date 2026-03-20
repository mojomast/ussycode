// Package db provides database models for exedevussy.
package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// SQLiteTime wraps time.Time to handle SQLite's string-based timestamp storage.
type SQLiteTime struct {
	time.Time
}

// Scan implements sql.Scanner for SQLite text timestamps.
func (t *SQLiteTime) Scan(value interface{}) error {
	if value == nil {
		t.Time = time.Time{}
		return nil
	}

	switch v := value.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			// Try alternate format without timezone
			parsed, err = time.Parse("2006-01-02T15:04:05Z", v)
			if err != nil {
				parsed, err = time.Parse("2006-01-02 15:04:05", v)
				if err != nil {
					return fmt.Errorf("parse time %q: %w", v, err)
				}
			}
		}
		t.Time = parsed
		return nil
	case time.Time:
		t.Time = v
		return nil
	default:
		return fmt.Errorf("unsupported time type: %T", value)
	}
}

// Value implements driver.Valuer.
func (t SQLiteTime) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.Time.UTC().Format(time.RFC3339), nil
}

// User represents a registered user identified by SSH key.
type User struct {
	ID         int64
	Handle     string
	TrustLevel string
	CreatedAt  SQLiteTime
	UpdatedAt  SQLiteTime
}

// SSHKey represents a user's SSH public key.
type SSHKey struct {
	ID          int64
	UserID      int64
	PublicKey   string // authorized_keys format
	Fingerprint string // SHA256 fingerprint
	Comment     string
	CreatedAt   SQLiteTime
}

// VM represents a virtual machine instance.
type VM struct {
	ID         int64
	UserID     int64
	Name       string
	Status     string // creating, running, stopped, error
	Image      string
	VCPU       int
	MemoryMB   int
	DiskGB     int
	TapDevice  sql.NullString
	IPAddress  sql.NullString
	MACAddress sql.NullString
	PID        sql.NullInt64
	CreatedAt  SQLiteTime
	UpdatedAt  SQLiteTime
}

// Share represents sharing permissions for a VM.
type Share struct {
	ID         int64
	VMID       int64
	SharedWith sql.NullInt64  // user ID, NULL for link-based
	LinkToken  sql.NullString // non-NULL for link-based sharing
	IsPublic   bool
	CreatedAt  SQLiteTime
}

// Token represents a short-lived API token.
type Token struct {
	ID        int64
	UserID    int64
	Handle    string // short opaque handle
	TokenData string // full signed token
	ExpiresAt SQLiteTime
	CreatedAt SQLiteTime
}

// IsExpired returns true if the token has expired.
func (t *Token) IsExpired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt.Time)
}
