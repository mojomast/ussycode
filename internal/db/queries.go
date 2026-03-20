// Package db provides query methods for exedevussy entities.
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
			`SELECT id, handle, trust_level, created_at, updated_at FROM users WHERE id = ?`, id,
		).Scan(&user.ID, &user.Handle, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
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
			`SELECT id, handle, trust_level, created_at, updated_at FROM users WHERE handle = ?`, handle,
		).Scan(&user.ID, &user.Handle, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
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
			`SELECT id, handle, trust_level, created_at, updated_at FROM users WHERE id = ?`, id,
		).Scan(&user.ID, &user.Handle, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
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
			`SELECT u.id, u.handle, u.trust_level, u.created_at, u.updated_at
			 FROM users u
			 JOIN ssh_keys k ON k.user_id = u.id
			 WHERE k.fingerprint = ?`, fingerprint,
		).Scan(&user.ID, &user.Handle, &user.TrustLevel, &user.CreatedAt, &user.UpdatedAt)
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
			if err := scanVMRow(rows, &vm); err != nil {
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVMRow(rows interface{ Scan(dest ...any) error }, vm *VM) error {
	return rows.Scan(
		&vm.ID, &vm.UserID, &vm.Name, &vm.Status, &vm.Image,
		&vm.VCPU, &vm.MemoryMB, &vm.DiskGB,
		&vm.TapDevice, &vm.IPAddress, &vm.MACAddress, &vm.PID,
		&vm.CreatedAt, &vm.UpdatedAt,
	)
}
