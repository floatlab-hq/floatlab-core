package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// BootstrapFile imports only new accounts. An existing account is never reset.
// The private source remains available for retries until the transaction succeeds.
func BootstrapFile(ctx context.Context, db *rqlite.Client, path string) error {
	var b credentials.Bootstrap
	if err := credentials.ReadPrivate(path, &b); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := credentials.ValidateBootstrap(b); err != nil {
		return err
	}
	now := time.Now().UTC()
	// All insertions check the absence of the user; inserting the user last makes
	// retries unable to restore consumed recovery codes or reset an existing factor.
	stmts := []rqlite.Statement{}
	for _, hash := range b.RecoveryHashes {
		stmts = append(stmts, rqlite.Statement{SQL: `INSERT INTO user_recovery_codes(username,code_hash) SELECT ?,? WHERE NOT EXISTS(SELECT 1 FROM users WHERE username=?)`, Params: []interface{}{b.Username, hash, b.Username}})
	}
	stmts = append(stmts,
		rqlite.Statement{SQL: `INSERT INTO user_credentials(username,totp_secret,last_totp_step) SELECT ?,?,-1 WHERE NOT EXISTS(SELECT 1 FROM users WHERE username=?)`, Params: []interface{}{b.Username, b.TOTPSecret, b.Username}},
		rqlite.Statement{SQL: `INSERT OR IGNORE INTO users(username,password_hash,roles,created_at,updated_at) VALUES(?,?,'["admin"]',?,?)`, Params: []interface{}{b.Username, b.PasswordHash, now, now}})

	if err := db.Execute(ctx, stmts); err != nil {
		return err
	}
	if err := credentials.WriteBytes(filepath.Join(filepath.Dir(path), "bootstrap-imported"), []byte("complete\n")); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove bootstrap credentials: %w", err)
	}
	return nil
}

// RegisterApplianceNode is an internal startup path, independent of user login or TOTP.
func RegisterApplianceNode(ctx context.Context, db *rqlite.Client, id, name string) error {
	now := time.Now().UTC()
	return db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO nodes(id,cluster_uuid,name,addresses,created_at,updated_at,hostname,role,zfs_pool) VALUES(?,'',?,'[]',?,?,?,'primary','floatlab')`, Params: []interface{}{id, name, now, now, name}}})
}
