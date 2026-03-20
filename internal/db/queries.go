// Package db provides query methods for ussycode entities.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// --- Users ---

// CreateUser inserts a new user and returns the created user.
func (d *DB) CreateUser(ctx context.Context, handle string) (*User, error) {
	var user User
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (handle) VALUES (?)`, handle)
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT id, handle, email, trust_level, created_at, updated_at FROM users WHERE id = ?`, id,
		).Scan(&user.ID, &user.Handle, &user.Email, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UserByHandle returns a user by handle.
func (d *DB) UserByHandle(ctx context.Context, handle string) (*User, error) {
	var user User
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, handle, email, trust_level, created_at, updated_at FROM users WHERE handle = ?`, handle,
		).Scan(&user.ID, &user.Handle, &user.Email, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UserByID returns a user by ID.
func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	var user User
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, handle, email, trust_level, created_at, updated_at FROM users WHERE id = ?`, id,
		).Scan(&user.ID, &user.Handle, &user.Email, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// --- SSH Keys ---

// AddSSHKey adds a public key for a user.
func (d *DB) AddSSHKey(ctx context.Context, userID int64, publicKey, fingerprint, comment string) (*SSHKey, error) {
	var key SSHKey
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment) VALUES (?, ?, ?, ?)`,
			userID, publicKey, fingerprint, comment)
		if err != nil {
			return fmt.Errorf("insert ssh key: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT id, user_id, public_key, fingerprint, comment, created_at FROM ssh_keys WHERE id = ?`, id,
		).Scan(&key.ID, &key.UserID, &key.PublicKey, &key.Fingerprint, &key.Comment, &key.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// UserByFingerprint looks up a user by SSH key fingerprint.
func (d *DB) UserByFingerprint(ctx context.Context, fingerprint string) (*User, error) {
	var user User
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT u.id, u.handle, u.email, u.trust_level, u.created_at, u.updated_at
			 FROM users u
			 JOIN ssh_keys k ON k.user_id = u.id
			 WHERE k.fingerprint = ?`, fingerprint,
		).Scan(&user.ID, &user.Handle, &user.Email, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// SSHKeysByUser returns all SSH keys for a user.
func (d *DB) SSHKeysByUser(ctx context.Context, userID int64) ([]*SSHKey, error) {
	var keys []*SSHKey
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, user_id, public_key, fingerprint, comment, created_at
			 FROM ssh_keys WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k SSHKey
			if err := rows.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.Fingerprint, &k.Comment, &k.CreatedAt); err != nil {
				return err
			}
			keys = append(keys, &k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteSSHKey removes an SSH key by its ID.
func (d *DB) DeleteSSHKey(ctx context.Context, keyID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ?`, keyID)
		return err
	})
}

// SSHKeyCountByUser returns how many keys a user has.
func (d *DB) SSHKeyCountByUser(ctx context.Context, userID int64) (int, error) {
	var count int
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ssh_keys WHERE user_id = ?`, userID,
		).Scan(&count)
	})
	return count, err
}

// --- VMs ---

// CreateVM inserts a new VM record.
func (d *DB) CreateVM(ctx context.Context, userID int64, name, image string, vcpu, memoryMB, diskGB int) (*VM, error) {
	var vm VM
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO vms (user_id, name, image, vcpu, memory_mb, disk_gb) VALUES (?, ?, ?, ?, ?, ?)`,
			userID, name, image, vcpu, memoryMB, diskGB)
		if err != nil {
			return fmt.Errorf("insert vm: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return scanVM(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
			        tap_device, ip_address, mac_address, pid, created_at, updated_at
			 FROM vms WHERE id = ?`, id), &vm)
	})
	if err != nil {
		return nil, err
	}
	return &vm, nil
}

// VMsByUser returns all VMs for a user.
func (d *DB) VMsByUser(ctx context.Context, userID int64) ([]*VM, error) {
	var vms []*VM
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
			        tap_device, ip_address, mac_address, pid, created_at, updated_at
			 FROM vms WHERE user_id = ? ORDER BY created_at DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var vm VM
			if err := scanVM(rows, &vm); err != nil {
				return err
			}
			vms = append(vms, &vm)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return vms, nil
}

// VMByUserAndName returns a specific VM by owner and name.
func (d *DB) VMByUserAndName(ctx context.Context, userID int64, name string) (*VM, error) {
	var vm VM
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return scanVM(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
			        tap_device, ip_address, mac_address, pid, created_at, updated_at
			 FROM vms WHERE user_id = ? AND name = ?`, userID, name), &vm)
	})
	if err != nil {
		return nil, err
	}
	return &vm, nil
}

// VMByIP returns a VM by its assigned IP address.
func (d *DB) VMByIP(ctx context.Context, ip string) (*VM, error) {
	var vm VM
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return scanVM(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
			        tap_device, ip_address, mac_address, pid, created_at, updated_at
			 FROM vms WHERE ip_address = ?`, ip), &vm)
	})
	if err != nil {
		return nil, err
	}
	return &vm, nil
}

// UpdateVMStatus updates a VM's status and optional runtime fields.
func (d *DB) UpdateVMStatus(ctx context.Context, vmID int64, status string, tapDevice, ipAddress, macAddress *string, pid *int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE vms SET status = ?, tap_device = ?, ip_address = ?, mac_address = ?, pid = ?,
			              updated_at = ? WHERE id = ?`,
			status, tapDevice, ipAddress, macAddress, pid,
			time.Now().UTC().Format(time.RFC3339), vmID)
		return err
	})
}

// DeleteVM removes a VM record.
func (d *DB) DeleteVM(ctx context.Context, vmID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM vms WHERE id = ?`, vmID)
		return err
	})
}

// RenameVM changes the name of a VM.
func (d *DB) RenameVM(ctx context.Context, vmID int64, newName string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE vms SET name = ?, updated_at = ? WHERE id = ?`,
			newName, time.Now().UTC().Format(time.RFC3339), vmID)
		return err
	})
}

// --- Tags ---

// AddTag adds a tag to a VM.
func (d *DB) AddTag(ctx context.Context, vmID int64, tag string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO vm_tags (vm_id, tag) VALUES (?, ?)`, vmID, tag)
		return err
	})
}

// RemoveTag removes a tag from a VM.
func (d *DB) RemoveTag(ctx context.Context, vmID int64, tag string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM vm_tags WHERE vm_id = ? AND tag = ?`, vmID, tag)
		return err
	})
}

// TagsByVM returns all tags for a VM.
func (d *DB) TagsByVM(ctx context.Context, vmID int64) ([]string, error) {
	var tags []string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT tag FROM vm_tags WHERE vm_id = ? ORDER BY tag`, vmID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tag string
			if err := rows.Scan(&tag); err != nil {
				return err
			}
			tags = append(tags, tag)
		}
		return rows.Err()
	})
	return tags, err
}

// --- Shares ---

// ShareVMWithUser creates a user-based share record.
func (d *DB) ShareVMWithUser(ctx context.Context, vmID, targetUserID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO shares (vm_id, shared_with) VALUES (?, ?)`,
			vmID, targetUserID)
		return err
	})
}

// ShareVMWithLink creates a link-based share record and returns it.
func (d *DB) ShareVMWithLink(ctx context.Context, vmID int64, linkToken string) (*Share, error) {
	var share Share
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO shares (vm_id, link_token) VALUES (?, ?)`,
			vmID, linkToken)
		if err != nil {
			return fmt.Errorf("insert link share: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT id, vm_id, shared_with, link_token, is_public, created_at
			 FROM shares WHERE id = ?`, id,
		).Scan(&share.ID, &share.VMID, &share.SharedWith, &share.LinkToken, &share.IsPublic, &share.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &share, nil
}

// RemoveShare deletes a share by ID.
func (d *DB) RemoveShare(ctx context.Context, shareID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM shares WHERE id = ?`, shareID)
		return err
	})
}

// RemoveShareByVMAndUser removes a user-based share.
func (d *DB) RemoveShareByVMAndUser(ctx context.Context, vmID, targetUserID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM shares WHERE vm_id = ? AND shared_with = ?`,
			vmID, targetUserID)
		return err
	})
}

// SharesByVM returns all share records for a VM.
func (d *DB) SharesByVM(ctx context.Context, vmID int64) ([]*Share, error) {
	var shares []*Share
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, vm_id, shared_with, link_token, is_public, created_at
			 FROM shares WHERE vm_id = ? ORDER BY created_at DESC`, vmID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Share
			if err := rows.Scan(&s.ID, &s.VMID, &s.SharedWith, &s.LinkToken, &s.IsPublic, &s.CreatedAt); err != nil {
				return err
			}
			shares = append(shares, &s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ShareByLinkToken looks up a share by its link token.
func (d *DB) ShareByLinkToken(ctx context.Context, token string) (*Share, error) {
	var share Share
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, vm_id, shared_with, link_token, is_public, created_at
			 FROM shares WHERE link_token = ?`, token,
		).Scan(&share.ID, &share.VMID, &share.SharedWith, &share.LinkToken, &share.IsPublic, &share.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &share, nil
}

// SetVMPublic sets the public flag for a VM. If no public share record exists,
// one is created (with both shared_with and link_token NULL).
func (d *DB) SetVMPublic(ctx context.Context, vmID int64, public bool) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		publicInt := 0
		if public {
			publicInt = 1
		}

		// Try to update existing public-flag share record
		res, err := tx.ExecContext(ctx,
			`UPDATE shares SET is_public = ?
			 WHERE vm_id = ? AND shared_with IS NULL AND link_token IS NULL`,
			publicInt, vmID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			return nil
		}

		// No existing record -- insert one
		_, err = tx.ExecContext(ctx,
			`INSERT INTO shares (vm_id, is_public) VALUES (?, ?)`,
			vmID, publicInt)
		return err
	})
}

// IsVMPublic checks if any share record for the VM has is_public=1.
func (d *DB) IsVMPublic(ctx context.Context, vmID int64) (bool, error) {
	var isPublic bool
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM shares WHERE vm_id = ? AND is_public = 1)`, vmID,
		).Scan(&isPublic)
	})
	return isPublic, err
}

// VMByName returns a VM by its name (without user filter).
// Used by the auth proxy to look up VMs by subdomain.
func (d *DB) VMByName(ctx context.Context, name string) (*VM, error) {
	var vm VM
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return scanVM(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, status, image, vcpu, memory_mb, disk_gb,
			        tap_device, ip_address, mac_address, pid, created_at, updated_at
			 FROM vms WHERE name = ?`, name), &vm)
	})
	if err != nil {
		return nil, err
	}
	return &vm, nil
}

// HasShareAccess checks if a user has share-based access to a VM.
func (d *DB) HasShareAccess(ctx context.Context, vmID, userID int64) (bool, error) {
	var has bool
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM shares WHERE vm_id = ? AND shared_with = ?)`,
			vmID, userID,
		).Scan(&has)
	})
	return has, err
}

// --- Tokens ---

// CreateToken stores a short token handle mapping to a full signed token.
func (d *DB) CreateToken(ctx context.Context, userID int64, handle, tokenData string, expiresAt time.Time) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO tokens (user_id, handle, token_data, expires_at) VALUES (?, ?, ?, ?)`,
			userID, handle, tokenData, expiresAt.UTC().Format(time.RFC3339))
		return err
	})
}

// TokenByHandle retrieves a token by its short handle.
func (d *DB) TokenByHandle(ctx context.Context, handle string) (*Token, error) {
	var tok Token
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, user_id, handle, token_data, expires_at, created_at
			 FROM tokens WHERE handle = ?`, handle,
		).Scan(&tok.ID, &tok.UserID, &tok.Handle, &tok.TokenData, &tok.ExpiresAt, &tok.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// CleanExpiredTokens removes tokens that have expired.
func (d *DB) CleanExpiredTokens(ctx context.Context) (int64, error) {
	var count int64
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM tokens WHERE expires_at < ?`,
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		count, err = res.RowsAffected()
		return err
	})
	return count, err
}

// --- Convenience queries for the SSH gateway ---

// HandleExists checks if a handle is already taken.
func (d *DB) HandleExists(ctx context.Context, handle string) (bool, error) {
	var exists bool
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE handle = ?)`, handle,
		).Scan(&exists)
	})
	return exists, err
}

// VMCountByUser returns the total number of VMs a user has.
func (d *DB) VMCountByUser(ctx context.Context, userID int64) (int, error) {
	var count int
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM vms WHERE user_id = ?`, userID,
		).Scan(&count)
	})
	return count, err
}

// RunningVMCountByUser returns the number of running VMs a user has.
func (d *DB) RunningVMCountByUser(ctx context.Context, userID int64) (int, error) {
	var count int
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM vms WHERE user_id = ? AND status = 'running'`, userID,
		).Scan(&count)
	})
	return count, err
}

// FingerprintByUser returns the fingerprint of the user's first SSH key.
func (d *DB) FingerprintByUser(ctx context.Context, userID int64) (string, error) {
	var fp string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT fingerprint FROM ssh_keys WHERE user_id = ? ORDER BY id ASC LIMIT 1`, userID,
		).Scan(&fp)
	})
	return fp, err
}

// --- Tutorial Progress ---

// GetTutorialProgress returns all completed lessons for a user, ordered by lesson number.
func (d *DB) GetTutorialProgress(ctx context.Context, userID int64) ([]TutorialProgress, error) {
	var progress []TutorialProgress
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, user_id, lesson_number, completed_at
			 FROM tutorial_progress WHERE user_id = ? ORDER BY lesson_number`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p TutorialProgress
			if err := rows.Scan(&p.ID, &p.UserID, &p.LessonNumber, &p.CompletedAt); err != nil {
				return err
			}
			progress = append(progress, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return progress, nil
}

// CompleteTutorialLesson marks a lesson as completed for a user.
// Uses INSERT OR IGNORE so completing the same lesson twice is idempotent.
func (d *DB) CompleteTutorialLesson(ctx context.Context, userID int64, lessonNumber int) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO tutorial_progress (user_id, lesson_number) VALUES (?, ?)`,
			userID, lessonNumber)
		return err
	})
}

// ResetTutorialProgress clears all tutorial progress for a user.
func (d *DB) ResetTutorialProgress(ctx context.Context, userID int64) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM tutorial_progress WHERE user_id = ?`, userID)
		return err
	})
}

// --- LLM Keys ---

// SetLLMKey stores or updates an encrypted API key for a user+provider.
// Uses UPSERT so calling with the same user+provider replaces the key.
func (d *DB) SetLLMKey(ctx context.Context, userID int64, provider, encryptedKey string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO llm_keys (user_id, provider, encrypted_key)
			 VALUES (?, ?, ?)
			 ON CONFLICT(user_id, provider) DO UPDATE SET
			   encrypted_key = excluded.encrypted_key,
			   updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`,
			userID, provider, encryptedKey)
		return err
	})
}

// GetLLMKey retrieves the encrypted API key for a user+provider.
func (d *DB) GetLLMKey(ctx context.Context, userID int64, provider string) (string, error) {
	var encKey string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT encrypted_key FROM llm_keys WHERE user_id = ? AND provider = ?`,
			userID, provider,
		).Scan(&encKey)
	})
	if err != nil {
		return "", err
	}
	return encKey, nil
}

// DeleteLLMKey removes the API key for a user+provider.
func (d *DB) DeleteLLMKey(ctx context.Context, userID int64, provider string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM llm_keys WHERE user_id = ? AND provider = ?`,
			userID, provider)
		return err
	})
}

// LLMKeyProvidersByUser returns the list of providers a user has configured keys for.
func (d *DB) LLMKeyProvidersByUser(ctx context.Context, userID int64) ([]string, error) {
	var providers []string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT provider FROM llm_keys WHERE user_id = ? ORDER BY provider`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			providers = append(providers, p)
		}
		return rows.Err()
	})
	return providers, err
}

// --- LLM Usage ---

// IncrementLLMUsage atomically increments the request count and estimated tokens
// for the current hourly period. Creates the row if it doesn't exist.
func (d *DB) IncrementLLMUsage(ctx context.Context, userID int64, provider string, requests, tokens int) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		periodStart := now.Truncate(time.Hour).Format(time.RFC3339)
		periodEnd := now.Truncate(time.Hour).Add(time.Hour).Format(time.RFC3339)

		_, err := tx.ExecContext(ctx,
			`INSERT INTO llm_usage (user_id, provider, request_count, estimated_tokens, period_start, period_end)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, provider, period_start) DO UPDATE SET
			   request_count = request_count + excluded.request_count,
			   estimated_tokens = estimated_tokens + excluded.estimated_tokens`,
			userID, provider, requests, tokens, periodStart, periodEnd)
		return err
	})
}

// GetLLMUsage returns usage stats for the current hourly period.
func (d *DB) GetLLMUsage(ctx context.Context, userID int64, provider string) (*LLMUsage, error) {
	var usage LLMUsage
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		periodStart := time.Now().UTC().Truncate(time.Hour).Format(time.RFC3339)
		return tx.QueryRowContext(ctx,
			`SELECT id, user_id, provider, request_count, estimated_tokens, period_start, period_end
			 FROM llm_usage WHERE user_id = ? AND provider = ? AND period_start = ?`,
			userID, provider, periodStart,
		).Scan(&usage.ID, &usage.UserID, &usage.Provider,
			&usage.RequestCount, &usage.EstimatedTokens,
			&usage.PeriodStart, &usage.PeriodEnd)
	})
	if err != nil {
		return nil, err
	}
	return &usage, nil
}

// --- User Email ---

// SetUserEmail updates the email address for a user.
func (d *DB) SetUserEmail(ctx context.Context, userID int64, email string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET email = ?, updated_at = ? WHERE id = ?`,
			email, time.Now().UTC().Format(time.RFC3339), userID)
		return err
	})
}

// GetUserEmail returns the email address for a user by their ID.
func (d *DB) GetUserEmail(ctx context.Context, userID int64) (string, error) {
	var email string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT email FROM users WHERE id = ?`, userID,
		).Scan(&email)
	})
	return email, err
}

// --- Trust Levels & Quotas ---

// GetUserTrustLevel returns the trust level for a user.
func (d *DB) GetUserTrustLevel(ctx context.Context, userID int64) (string, error) {
	var level string
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT trust_level FROM users WHERE id = ?`, userID,
		).Scan(&level)
	})
	return level, err
}

// SetUserTrustLevel updates the trust level for a user and resets their
// quota columns to match the new level's defaults.
func (d *DB) SetUserTrustLevel(ctx context.Context, userID int64, level string) error {
	if !IsValidTrustLevel(level) {
		return fmt.Errorf("invalid trust level %q", level)
	}
	limits := GetTrustLimits(level)
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET trust_level = ?, vm_limit = ?, cpu_limit = ?, ram_limit_mb = ?, disk_limit_mb = ?,
			                  updated_at = ? WHERE id = ?`,
			level, limits.VMLimit, limits.CPULimit, limits.RAMLimit, limits.DiskLimit,
			time.Now().UTC().Format(time.RFC3339), userID)
		return err
	})
}

// GetUserVMCount returns the total number of VMs a user has.
// This is an alias for VMCountByUser for consistency with the trust/quota API.
func (d *DB) GetUserVMCount(ctx context.Context, userID int64) (int, error) {
	return d.VMCountByUser(ctx, userID)
}

// GetUserQuotas returns the per-user quota columns (vm_limit, cpu_limit, etc.).
// These may differ from the trust-level defaults if they were customized.
func (d *DB) GetUserQuotas(ctx context.Context, userID int64) (*TrustLimits, error) {
	var limits TrustLimits
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT trust_level, vm_limit, cpu_limit, ram_limit_mb, disk_limit_mb
			 FROM users WHERE id = ?`, userID,
		).Scan(&limits.Level, &limits.VMLimit, &limits.CPULimit, &limits.RAMLimit, &limits.DiskLimit)
	})
	if err != nil {
		return nil, err
	}
	return &limits, nil
}

// --- Custom Domains ---

// CreateCustomDomain inserts a new custom domain mapping for a VM.
func (d *DB) CreateCustomDomain(ctx context.Context, vmID int64, domain, verificationToken string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO custom_domains (vm_id, domain, verification_token) VALUES (?, ?, ?)`,
			vmID, domain, verificationToken)
		if err != nil {
			return fmt.Errorf("insert custom domain: %w", err)
		}
		return nil
	})
}

// GetCustomDomain returns a custom domain record by its domain name.
func (d *DB) GetCustomDomain(ctx context.Context, domain string) (*CustomDomain, error) {
	var cd CustomDomain
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, vm_id, domain, verified, verification_token, created_at, verified_at
			 FROM custom_domains WHERE domain = ?`, domain,
		).Scan(&cd.ID, &cd.VMID, &cd.Domain, &cd.Verified, &cd.VerificationToken,
			&cd.CreatedAt, &cd.VerifiedAt)
	})
	if err != nil {
		return nil, err
	}
	return &cd, nil
}

// VerifyCustomDomain marks a custom domain as verified.
func (d *DB) VerifyCustomDomain(ctx context.Context, domain string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE custom_domains SET verified = TRUE, verified_at = ? WHERE domain = ?`,
			time.Now().UTC().Format(time.RFC3339), domain)
		return err
	})
}

// ListCustomDomains returns all custom domains for a VM.
func (d *DB) ListCustomDomains(ctx context.Context, vmID int64) ([]CustomDomain, error) {
	var domains []CustomDomain
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, vm_id, domain, verified, verification_token, created_at, verified_at
			 FROM custom_domains WHERE vm_id = ? ORDER BY created_at DESC`, vmID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cd CustomDomain
			if err := rows.Scan(&cd.ID, &cd.VMID, &cd.Domain, &cd.Verified,
				&cd.VerificationToken, &cd.CreatedAt, &cd.VerifiedAt); err != nil {
				return err
			}
			domains = append(domains, cd)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return domains, nil
}

// DeleteCustomDomain removes a custom domain record by domain name.
func (d *DB) DeleteCustomDomain(ctx context.Context, domain string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM custom_domains WHERE domain = ?`, domain)
		return err
	})
}

// --- scan helpers ---

// --- API Tokens (usy1.) ---

// CreateAPIToken stores a short (usy1.) API token.
func (d *DB) CreateAPIToken(ctx context.Context, tokenID string, userID int64, fullToken, description string) (*APIToken, error) {
	var tok APIToken
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO api_tokens (token_id, user_id, full_token, description)
			 VALUES (?, ?, ?, ?)`,
			tokenID, userID, fullToken, description)
		if err != nil {
			return fmt.Errorf("insert api token: %w", err)
		}
		return tx.QueryRowContext(ctx,
			`SELECT token_id, user_id, full_token, description, created_at, last_used_at, revoked
			 FROM api_tokens WHERE token_id = ?`, tokenID,
		).Scan(&tok.TokenID, &tok.UserID, &tok.FullToken, &tok.Description,
			&tok.CreatedAt, &tok.LastUsedAt, &tok.Revoked)
	})
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// APITokenByID retrieves an API token by its token_id.
func (d *DB) APITokenByID(ctx context.Context, tokenID string) (*APIToken, error) {
	var tok APIToken
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT token_id, user_id, full_token, description, created_at, last_used_at, revoked
			 FROM api_tokens WHERE token_id = ?`, tokenID,
		).Scan(&tok.TokenID, &tok.UserID, &tok.FullToken, &tok.Description,
			&tok.CreatedAt, &tok.LastUsedAt, &tok.Revoked)
	})
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// APITokensByUser returns all API tokens for a user.
func (d *DB) APITokensByUser(ctx context.Context, userID int64) ([]*APIToken, error) {
	var tokens []*APIToken
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT token_id, user_id, full_token, description, created_at, last_used_at, revoked
			 FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t APIToken
			if err := rows.Scan(&t.TokenID, &t.UserID, &t.FullToken, &t.Description,
				&t.CreatedAt, &t.LastUsedAt, &t.Revoked); err != nil {
				return err
			}
			tokens = append(tokens, &t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// RevokeAPIToken marks an API token as revoked.
func (d *DB) RevokeAPIToken(ctx context.Context, tokenID string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE api_tokens SET revoked = 1 WHERE token_id = ?`, tokenID)
		return err
	})
}

// TouchAPIToken updates the last_used_at timestamp for an API token.
func (d *DB) TouchAPIToken(ctx context.Context, tokenID string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE api_tokens SET last_used_at = ? WHERE token_id = ?`,
			time.Now().UTC().Format(time.RFC3339), tokenID)
		return err
	})
}

// DeleteAPIToken removes an API token.
func (d *DB) DeleteAPIToken(ctx context.Context, tokenID string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE token_id = ?`, tokenID)
		return err
	})
}

// --- Magic Tokens ---

// CreateMagicToken stores a one-time-use magic link token.
func (d *DB) CreateMagicToken(ctx context.Context, userID int64, token string, expiresAt time.Time) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO magic_tokens (token, user_id, expires_at) VALUES (?, ?, ?)`,
			token, userID, expiresAt.UTC().Format(time.RFC3339))
		return err
	})
}

// ValidateMagicToken checks if a magic token is valid (exists, not expired, not used),
// marks it as used, and returns the associated user. Returns an error if the token
// is invalid, expired, or already used.
func (d *DB) ValidateMagicToken(ctx context.Context, token string) (*User, error) {
	var user User
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		// Look up the token
		var userID int64
		var expiresAt string
		var used int
		err := tx.QueryRowContext(ctx,
			`SELECT user_id, expires_at, used FROM magic_tokens WHERE token = ?`, token,
		).Scan(&userID, &expiresAt, &used)
		if err != nil {
			return fmt.Errorf("token not found: %w", err)
		}

		// Check if already used
		if used != 0 {
			return fmt.Errorf("token already used")
		}

		// Check if expired
		expiry, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return fmt.Errorf("parse expiry: %w", err)
		}
		if time.Now().UTC().After(expiry) {
			return fmt.Errorf("token expired")
		}

		// Mark as used
		if _, err := tx.ExecContext(ctx,
			`UPDATE magic_tokens SET used = 1 WHERE token = ?`, token); err != nil {
			return fmt.Errorf("mark used: %w", err)
		}

		// Fetch the user
		return tx.QueryRowContext(ctx,
			`SELECT id, handle, email, trust_level, created_at, updated_at FROM users WHERE id = ?`, userID,
		).Scan(&user.ID, &user.Handle, &user.Email, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// CleanExpiredMagicTokens removes expired or used magic tokens older than 1 hour.
func (d *DB) CleanExpiredMagicTokens(ctx context.Context) (int64, error) {
	var count int64
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM magic_tokens WHERE used = 1 OR expires_at < ?`,
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		count, err = res.RowsAffected()
		return err
	})
	return count, err
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanVM(row scannable, vm *VM) error {
	return row.Scan(
		&vm.ID, &vm.UserID, &vm.Name, &vm.Status, &vm.Image,
		&vm.VCPU, &vm.MemoryMB, &vm.DiskGB,
		&vm.TapDevice, &vm.IPAddress, &vm.MACAddress, &vm.PID,
		&vm.CreatedAt, &vm.UpdatedAt,
	)
}

// --- Arena Matches ---

// CreateArenaMatch inserts a new arena match and returns it.
func (d *DB) CreateArenaMatch(ctx context.Context, matchID, scenario string, maxAgents int, createdBy int64) (*ArenaMatch, error) {
	var m ArenaMatch
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO arena_matches (match_id, scenario, max_agents, created_by)
			 VALUES (?, ?, ?, ?)`,
			matchID, scenario, maxAgents, createdBy)
		if err != nil {
			return fmt.Errorf("insert arena match: %w", err)
		}
		return tx.QueryRowContext(ctx,
			`SELECT id, match_id, scenario, status, max_agents, created_by,
			        started_at, completed_at, created_at
			 FROM arena_matches WHERE match_id = ?`, matchID,
		).Scan(&m.ID, &m.MatchID, &m.Scenario, &m.Status, &m.MaxAgents,
			&m.CreatedBy, &m.StartedAt, &m.CompletedAt, &m.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetArenaMatch returns a match by its match_id.
func (d *DB) GetArenaMatch(ctx context.Context, matchID string) (*ArenaMatch, error) {
	var m ArenaMatch
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT id, match_id, scenario, status, max_agents, created_by,
			        started_at, completed_at, created_at
			 FROM arena_matches WHERE match_id = ?`, matchID,
		).Scan(&m.ID, &m.MatchID, &m.Scenario, &m.Status, &m.MaxAgents,
			&m.CreatedBy, &m.StartedAt, &m.CompletedAt, &m.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListArenaMatches returns active matches (waiting or running), ordered by creation.
func (d *DB) ListArenaMatches(ctx context.Context) ([]*ArenaMatch, error) {
	var matches []*ArenaMatch
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, match_id, scenario, status, max_agents, created_by,
			        started_at, completed_at, created_at
			 FROM arena_matches WHERE status IN ('waiting', 'running')
			 ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ArenaMatch
			if err := rows.Scan(&m.ID, &m.MatchID, &m.Scenario, &m.Status, &m.MaxAgents,
				&m.CreatedBy, &m.StartedAt, &m.CompletedAt, &m.CreatedAt); err != nil {
				return err
			}
			matches = append(matches, &m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// ListArenaMatchHistory returns completed/cancelled matches for a user.
func (d *DB) ListArenaMatchHistory(ctx context.Context, userID int64) ([]*ArenaMatch, error) {
	var matches []*ArenaMatch
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT DISTINCT m.id, m.match_id, m.scenario, m.status, m.max_agents, m.created_by,
			        m.started_at, m.completed_at, m.created_at
			 FROM arena_matches m
			 LEFT JOIN arena_participants p ON p.match_id = m.match_id
			 WHERE m.status IN ('completed', 'cancelled')
			   AND (m.created_by = ? OR p.user_id = ?)
			 ORDER BY m.created_at DESC
			 LIMIT 50`, userID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ArenaMatch
			if err := rows.Scan(&m.ID, &m.MatchID, &m.Scenario, &m.Status, &m.MaxAgents,
				&m.CreatedBy, &m.StartedAt, &m.CompletedAt, &m.CreatedAt); err != nil {
				return err
			}
			matches = append(matches, &m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// UpdateArenaMatchStatus updates a match's status and optional timestamps.
func (d *DB) UpdateArenaMatchStatus(ctx context.Context, matchID, status string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		switch status {
		case "running":
			_, err := tx.ExecContext(ctx,
				`UPDATE arena_matches SET status = ?, started_at = ? WHERE match_id = ?`,
				status, now, matchID)
			return err
		case "completed", "cancelled":
			_, err := tx.ExecContext(ctx,
				`UPDATE arena_matches SET status = ?, completed_at = ? WHERE match_id = ?`,
				status, now, matchID)
			return err
		default:
			_, err := tx.ExecContext(ctx,
				`UPDATE arena_matches SET status = ? WHERE match_id = ?`,
				status, matchID)
			return err
		}
	})
}

// --- Arena Participants ---

// JoinArenaMatch adds a user as a participant in a match.
func (d *DB) JoinArenaMatch(ctx context.Context, matchID string, userID int64) (*ArenaParticipant, error) {
	var p ArenaParticipant
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO arena_participants (match_id, user_id) VALUES (?, ?)`,
			matchID, userID)
		if err != nil {
			return fmt.Errorf("join arena match: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT id, match_id, user_id, vm_id, score, status, joined_at
			 FROM arena_participants WHERE id = ?`, id,
		).Scan(&p.ID, &p.MatchID, &p.UserID, &p.VMID, &p.Score, &p.Status, &p.JoinedAt)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListArenaParticipants returns all participants for a match.
func (d *DB) ListArenaParticipants(ctx context.Context, matchID string) ([]*ArenaParticipant, error) {
	var participants []*ArenaParticipant
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, match_id, user_id, vm_id, score, status, joined_at
			 FROM arena_participants WHERE match_id = ?
			 ORDER BY score DESC, joined_at ASC`, matchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ArenaParticipant
			if err := rows.Scan(&p.ID, &p.MatchID, &p.UserID, &p.VMID,
				&p.Score, &p.Status, &p.JoinedAt); err != nil {
				return err
			}
			participants = append(participants, &p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return participants, nil
}

// UpdateArenaParticipantScore updates a participant's score in a match.
func (d *DB) UpdateArenaParticipantScore(ctx context.Context, matchID string, userID int64, score int) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE arena_participants SET score = ? WHERE match_id = ? AND user_id = ?`,
			score, matchID, userID)
		return err
	})
}

// UpdateArenaParticipantStatus updates a participant's status in a match.
func (d *DB) UpdateArenaParticipantStatus(ctx context.Context, matchID string, userID int64, status string) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE arena_participants SET status = ? WHERE match_id = ? AND user_id = ?`,
			status, matchID, userID)
		return err
	})
}

// ArenaParticipantCount returns the number of participants in a match.
func (d *DB) ArenaParticipantCount(ctx context.Context, matchID string) (int, error) {
	var count int
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM arena_participants WHERE match_id = ?`, matchID,
		).Scan(&count)
	})
	return count, err
}

// IsArenaParticipant checks if a user is already a participant in a match.
func (d *DB) IsArenaParticipant(ctx context.Context, matchID string, userID int64) (bool, error) {
	var exists bool
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM arena_participants WHERE match_id = ? AND user_id = ?)`,
			matchID, userID,
		).Scan(&exists)
	})
	return exists, err
}

// --- Arena ELO ---

// GetArenaELO returns the ELO record for a user. Returns a default 1200 rating if none exists.
func (d *DB) GetArenaELO(ctx context.Context, userID int64) (*ArenaELO, error) {
	var elo ArenaELO
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT user_id, rating, wins, losses, draws, last_match_at
			 FROM arena_elo WHERE user_id = ?`, userID,
		).Scan(&elo.UserID, &elo.Rating, &elo.Wins, &elo.Losses, &elo.Draws, &elo.LastMatchAt)
	})
	if err != nil {
		if err == sql.ErrNoRows {
			// Return default rating
			return &ArenaELO{UserID: userID, Rating: 1200}, nil
		}
		return nil, err
	}
	return &elo, nil
}

// UpdateArenaELO upserts a user's ELO record.
func (d *DB) UpdateArenaELO(ctx context.Context, userID int64, rating, wins, losses, draws int) error {
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := tx.ExecContext(ctx,
			`INSERT INTO arena_elo (user_id, rating, wins, losses, draws, last_match_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id) DO UPDATE SET
			   rating = excluded.rating,
			   wins = excluded.wins,
			   losses = excluded.losses,
			   draws = excluded.draws,
			   last_match_at = excluded.last_match_at`,
			userID, rating, wins, losses, draws, now)
		return err
	})
}

// GetArenaLeaderboard returns the top N players by ELO rating.
func (d *DB) GetArenaLeaderboard(ctx context.Context, limit int) ([]*ArenaELO, error) {
	var entries []*ArenaELO
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT user_id, rating, wins, losses, draws, last_match_at
			 FROM arena_elo ORDER BY rating DESC LIMIT ?`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e ArenaELO
			if err := rows.Scan(&e.UserID, &e.Rating, &e.Wins, &e.Losses, &e.Draws, &e.LastMatchAt); err != nil {
				return err
			}
			entries = append(entries, &e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// GetArenaRank returns a user's rank on the leaderboard (1-indexed).
func (d *DB) GetArenaRank(ctx context.Context, userID int64) (int, error) {
	var rank int
	err := d.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) + 1 FROM arena_elo
			 WHERE rating > (SELECT COALESCE((SELECT rating FROM arena_elo WHERE user_id = ?), 1200))`,
			userID,
		).Scan(&rank)
	})
	return rank, err
}
