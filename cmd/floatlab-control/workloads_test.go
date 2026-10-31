package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"golang.org/x/crypto/bcrypt"
)

func TestJoinedLabRetiresStandaloneBootstrapAccounts(t *testing.T) {
	for _, mode := range []string{"flags", "file"} {
		t.Run(mode, func(t *testing.T) {
			oldDir, oldUser, oldPass, oldFile, oldRegister := labDataDir, bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode
			t.Cleanup(func() {
				labDataDir, bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode = oldDir, oldUser, oldPass, oldFile, oldRegister
			})
			labDataDir = t.TempDir()
			bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode = "old-admin", "standalone-password", "", false
			var bootstrap credentials.Bootstrap
			if mode == "file" {
				bootstrapUser, bootstrapPass = "", ""
				bootstrapFile = filepath.Join(labDataDir, "bootstrap.json")
				hash, err := bcrypt.GenerateFromPassword([]byte("standalone-password"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				bootstrap = credentials.Bootstrap{Username: "old-admin", PasswordHash: string(hash), NodeID: "owner", NodeName: "owner", TOTPSecret: "encrypted", RecoveryHashes: []string{"hash"}}
				if err := credentials.WritePrivate(bootstrapFile, bootstrap); err != nil {
					t.Fatal(err)
				}
			}
			db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "config.sqlite"), "owner")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if err := initializeLab(ctx, db); err != nil {
				t.Fatal(err)
			}
			users, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT username FROM users`})
			if err != nil || len(users.Values) != 1 || users.Values[0][0] != "old-admin" {
				t.Fatal("standalone bootstrap failed", users, err)
			}
			if mode == "file" {
				if _, err := os.Stat(bootstrapFile); !os.IsNotExist(err) {
					t.Fatal("standalone bootstrap did not consume private source", err)
				}
				if err := credentials.WritePrivate(bootstrapFile, bootstrap); err != nil {
					t.Fatal(err)
				}
			}
			// Enrollment replaces old standalone users and persists imported secrets.
			if err := db.Execute(ctx, []rqlite.Statement{{SQL: `DELETE FROM user_credentials`}, {SQL: `DELETE FROM user_recovery_codes`}, {SQL: `UPDATE users SET username='lab-admin' WHERE username='old-admin'`}}); err != nil {
				t.Fatal(err)
			}
			if err := credentials.WritePrivate(filepath.Join(labDataDir, "auth.json"), map[string]string{"jwt_secret": "imported-lab-authentication-secret"}); err != nil {
				t.Fatal(err)
			}
			if err := initializeLab(ctx, db); err != nil {
				t.Fatal(err)
			}
			users, err = db.Query(ctx, rqlite.Statement{SQL: `SELECT username FROM users`})
			if err != nil || len(users.Values) != 1 || users.Values[0][0] != "lab-admin" {
				t.Fatal("joined restart recreated discarded standalone admin", users, err)
			}
		})
	}
}

func TestStandaloneMigrationRetainsBackupAndSwitchesAuthorityOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	oldPath, oldID := workloadDBPath, hostNodeID
	workloadDBPath, hostNodeID = filepath.Join(dir, "workloads", "local.sqlite"), "owner"
	t.Cleanup(func() { workloadDBPath, hostNodeID = oldPath, oldID })
	source, err := rqlite.OpenLocal(filepath.Join(dir, "source.sqlite"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err = rqlite.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	stack := &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "owner", ComposeYAML: "services: {}", FailoverMode: "manual"}
	if err = config.NewStore(source).CreateStack(ctx, stack); err != nil {
		t.Fatal(err)
	}
	local, err := openWorkloads(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.NewStore(local).GetStack(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	local.Close()
	var backup struct {
		NodeID string                `json:"node_id"`
		Tables []string              `json:"tables"`
		Rows   []*rqlite.QueryResult `json:"rows"`
	}
	if err = credentials.ReadPrivate(filepath.Join(dir, "workloads", "legacy-rqlite-backup.json"), &backup); err != nil {
		t.Fatal(err)
	}
	if backup.NodeID != "owner" || len(backup.Tables) != len(workloadTables) || len(backup.Rows) != len(workloadTables) {
		t.Fatalf("incomplete source backup: %+v", backup)
	}
	if err = source.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE stacks SET name='obsolete-source' WHERE id='web'`}}); err != nil {
		t.Fatal(err)
	}
	local, err = openWorkloads(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	actual, err := config.NewStore(local).GetStack(ctx, "web")
	if err != nil || actual.Name != "web" {
		t.Fatalf("old source regained workload authority: %+v %v", actual, err)
	}
}
