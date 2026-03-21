// Package db provides database models for ussycode.
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
	case []byte:
		return t.Scan(string(v))
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
	Email      string // optional email address for the user
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

// TutorialProgress tracks which tutorial lessons a user has completed.
type TutorialProgress struct {
	ID           int64
	UserID       int64
	LessonNumber int
	CompletedAt  SQLiteTime
}

// APIToken represents a short (usy1.) API token stored in the database.
type APIToken struct {
	TokenID     string
	UserID      int64
	FullToken   string
	Description sql.NullString
	CreatedAt   SQLiteTime
	LastUsedAt  SQLiteTime
	Revoked     bool
}

// LLMKey represents a stored, encrypted API key for an LLM provider.
type LLMKey struct {
	ID           int64
	UserID       int64
	Provider     string
	EncryptedKey string
	CreatedAt    SQLiteTime
	UpdatedAt    SQLiteTime
}

// LLMUsage tracks per-user LLM usage within a time period.
type LLMUsage struct {
	ID              int64
	UserID          int64
	Provider        string
	RequestCount    int
	EstimatedTokens int
	PeriodStart     SQLiteTime
	PeriodEnd       SQLiteTime
}

// MagicToken represents a one-time-use authentication token for browser access.
type MagicToken struct {
	Token     string
	UserID    int64
	ExpiresAt SQLiteTime
	Used      bool
}

// ArenaMatch represents a CTF/agent competition match.
type ArenaMatch struct {
	ID          int64
	MatchID     string
	Scenario    string
	Status      string // waiting, running, completed, cancelled
	MaxAgents   int
	CreatedBy   int64
	StartedAt   SQLiteTime
	CompletedAt SQLiteTime
	CreatedAt   SQLiteTime
}

// ArenaParticipant represents a user participating in an arena match.
type ArenaParticipant struct {
	ID       int64
	MatchID  string
	UserID   int64
	VMID     sql.NullInt64
	Score    int
	Status   string // joined, ready, playing, finished, disconnected
	JoinedAt SQLiteTime
}

// ArenaELO represents a user's ELO rating for arena competition.
type ArenaELO struct {
	UserID      int64
	Rating      int
	Wins        int
	Losses      int
	Draws       int
	LastMatchAt SQLiteTime
}

// TrustLimits defines the resource quotas for a given trust level.
type TrustLimits struct {
	Level     string
	VMLimit   int // max number of VMs (-1 = unlimited)
	CPULimit  int // max vCPUs per VM (-1 = unlimited)
	RAMLimit  int // max RAM in MB (-1 = unlimited)
	DiskLimit int // max disk in MB (-1 = unlimited)
}

// ValidTrustLevels lists all valid trust level strings.
var ValidTrustLevels = []string{"newbie", "citizen", "operator", "admin"}

// IsValidTrustLevel returns true if level is a recognized trust level.
func IsValidTrustLevel(level string) bool {
	for _, l := range ValidTrustLevels {
		if l == level {
			return true
		}
	}
	return false
}

// trustLimitsMap maps trust levels to their resource quotas.
var trustLimitsMap = map[string]TrustLimits{
	"newbie": {
		Level:     "newbie",
		VMLimit:   3,
		CPULimit:  1,
		RAMLimit:  2048,
		DiskLimit: 5120,
	},
	"citizen": {
		Level:     "citizen",
		VMLimit:   10,
		CPULimit:  4,
		RAMLimit:  8192,
		DiskLimit: 25600,
	},
	"operator": {
		Level:     "operator",
		VMLimit:   25,
		CPULimit:  8,
		RAMLimit:  16384,
		DiskLimit: 102400,
	},
	"admin": {
		Level:     "admin",
		VMLimit:   -1,
		CPULimit:  -1,
		RAMLimit:  -1,
		DiskLimit: -1,
	},
}

// GetTrustLimits returns the resource quotas for a given trust level.
// Returns newbie limits if the level is unrecognized.
func GetTrustLimits(level string) TrustLimits {
	if limits, ok := trustLimitsMap[level]; ok {
		return limits
	}
	return trustLimitsMap["newbie"]
}

// CustomDomain represents a custom domain mapped to a VM.
type CustomDomain struct {
	ID                int64
	VMID              int64
	Domain            string
	Verified          bool
	VerificationToken sql.NullString
	CreatedAt         SQLiteTime
	VerifiedAt        SQLiteTime
}
