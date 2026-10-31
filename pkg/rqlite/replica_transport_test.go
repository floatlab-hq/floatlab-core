package rqlite

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestReplicaSSHHelper exercises the forced-command entry point without needing
// SSH credentials; appliance integration tests separately verify real SSH trust.
func TestReplicaSSHHelper(t *testing.T) {
	if os.Getenv("FLOATLAB_REPLICA_HELPER") != "1" {
		return
	}
	receiver := ReplicaReceiver{Root: os.Getenv("FLOATLAB_REPLICA_ROOT"), SourceNodeID: "node-a", SQLiteRsync: SQLiteRsync{Executable: os.Getenv("FLOATLAB_TEST_SQLITE_RSYNC"), Version: "3.50.4"}}
	err := ServeReplicaCommand(context.Background(), receiver, os.Args[len(os.Args)-1], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestNativeSQLiteRsyncRoundTrip(t *testing.T) {
	binary := os.Getenv("FLOATLAB_TEST_SQLITE_RSYNC")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("sqlite3_rsync")
		if err != nil {
			t.Skip("pinned sqlite3_rsync unavailable; set FLOATLAB_TEST_SQLITE_RSYNC")
		}
	}
	transport := SQLiteRsync{Executable: binary, Version: "3.50.4"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := transport.check(ctx); err != nil {
		t.Fatal(err)
	}
	db := localForTest(t, "node-a")
	if err := db.Execute(ctx, []Statement{{SQL: `CREATE TABLE payload(id TEXT PRIMARY KEY,value TEXT)`}, {SQL: `INSERT INTO payload VALUES('old','before')`}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + executable + "' -test.run '^TestReplicaSSHHelper$' -- \"$@\"\n"
	if err = os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FLOATLAB_REPLICA_HELPER", "1")
	t.Setenv("FLOATLAB_REPLICA_ROOT", root)
	t.Setenv("FLOATLAB_TEST_SQLITE_RSYNC", binary)
	peer := ReplicaPeer{NodeID: "node-b", Address: "127.0.0.1", SSHUser: "replica", IdentityFile: "key", KnownHostsFile: "known_hosts"}
	got, err := transport.Transfer(ctx, peer, db.LocalPath())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := db.LocalIdentity(ctx)
	if got != want {
		t.Fatalf("published %v, want %v", got, want)
	}
	if err = db.Execute(ctx, []Statement{{SQL: `DELETE FROM payload`}, {SQL: `INSERT INTO payload VALUES('new','after')`}}); err != nil {
		t.Fatal(err)
	}
	got, err = transport.Transfer(ctx, peer, db.LocalPath())
	if err != nil {
		t.Fatal(err)
	}
	want, _ = db.LocalIdentity(ctx)
	if got != want {
		t.Fatalf("incremental published %v, want %v", got, want)
	}
	replica, err := OpenReplica(filepath.Join(root, "node-a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	rows, err := replica.Query(ctx, Statement{SQL: `SELECT id,value FROM payload`})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 || rows.Values[0][0] != "new" || rows.Values[0][1] != "after" {
		t.Fatalf("replica content %#v", rows)
	}
}

func TestReplicaReceiverDeniesArbitraryCommands(t *testing.T) {
	for _, command := range []string{"sh", "replicate other-node", "status /etc/passwd", "sqlite3_rsync --replica /tmp/local.sqlite"} {
		if err := ServeReplicaCommand(context.Background(), ReplicaReceiver{Root: t.TempDir(), SourceNodeID: "node-a"}, command, nil, nil, nil); err == nil {
			t.Fatalf("accepted %q", command)
		}
	}
}
