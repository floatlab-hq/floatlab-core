package rqlite

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func localForTest(t *testing.T, node string) *Client {
	t.Helper()
	c, err := OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestLocalTransactionCommitRollbackAndIdentity(t *testing.T) {
	ctx := context.Background()
	c := localForTest(t, "node-a")
	if err := Migrate(ctx, c); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-c.Changes():
			continue
		default:
		}
		break
	}
	before, err := c.LocalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Execute(ctx, []Statement{{SQL: `INSERT INTO scheduler_state(stack_id,tier) VALUES('one','1h')`}, {SQL: `INSERT INTO scheduler_state(stack_id,tier) VALUES('one','1h')`}}); err == nil {
		t.Fatal("expected unique violation")
	}
	after, _ := c.LocalIdentity(ctx)
	if before != after {
		t.Fatalf("rollback changed revision: %v %v", before, after)
	}
	select {
	case <-c.Changes():
		t.Fatal("rollback notified")
	default:
	}
	result, err := c.Request(ctx, Statement{SQL: `INSERT INTO scheduler_state(stack_id,tier) VALUES('one','1h') RETURNING stack_id`})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected != 1 || result.Values[0][0] != "one" {
		t.Fatalf("request result: %#v", result)
	}
	select {
	case <-c.Changes():
	default:
		t.Fatal("missing postcommit notification")
	}
	after, _ = c.LocalIdentity(ctx)
	if after.Revision != before.Revision+1 {
		t.Fatal(after)
	}
	result, err = c.QueryStrong(ctx, Statement{SQL: `SELECT count(*) FROM scheduler_state`})
	if err != nil || result.Values[0][0] != float64(1) {
		t.Fatalf("numeric compatibility: %v %v", result, err)
	}
	path := c.LocalPath()
	c.Close()
	reopened, err := OpenLocal(path, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, _ := reopened.LocalIdentity(ctx)
	if persisted != after {
		t.Fatal("identity not durable")
	}
	if wrong, err := OpenLocal(path, "node-b"); err == nil {
		wrong.Close()
		t.Fatal("accepted wrong owner")
	}
}

func TestCopyTablesAndReplicaPublication(t *testing.T) {
	ctx := context.Background()
	source := localForTest(t, "node-a")
	target := localForTest(t, "node-b")
	for _, c := range []*Client{source, target} {
		if err := Migrate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Execute(ctx, []Statement{{SQL: `INSERT INTO users VALUES ('alice','hash','[]','now','now')`}}); err != nil {
		t.Fatal(err)
	}
	before, _ := target.LocalIdentity(ctx)
	if err := CopyTables(ctx, source, target, []string{"users"}); err != nil {
		t.Fatal(err)
	}
	after, _ := target.LocalIdentity(ctx)
	if before != after {
		t.Fatal("cache created replication feedback")
	}
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage.sqlite")
	published := filepath.Join(dir, "node-a.sqlite")
	if _, err := source.local.db.ExecContext(ctx, `VACUUM INTO ?`, stage); err != nil {
		t.Fatal(err)
	}
	if err := PublishReplica(ctx, stage, published, "node-a"); err != nil {
		t.Fatal(err)
	}
	replica, err := OpenReplica(published)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	if err = replica.Execute(ctx, []Statement{{SQL: `DELETE FROM users`}}); err == nil {
		t.Fatal("replica accepted mutation")
	}
	if err := os.WriteFile(stage, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PublishReplica(ctx, stage, published, "node-a"); err == nil {
		t.Fatal("accepted corruption")
	}
	if _, err := ReadReplicaIdentity(ctx, published); err != nil {
		t.Fatal("last good replica lost", err)
	}
}

func TestReplicatorCommitDrivenCatchupAndRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := localForTest(t, "node-a")
	var calls atomic.Int64
	r := &Replicator{DB: db, StateDir: t.TempDir(), Peers: func(context.Context) ([]ReplicaPeer, error) { return []ReplicaPeer{{NodeID: "node-b"}}, nil }, Transfer: func(ctx context.Context, p ReplicaPeer, path string) (LocalIdentity, error) {
		calls.Add(1)
		return db.LocalIdentity(ctx)
	}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("startup catchup did not run")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := db.Execute(ctx, []Statement{{SQL: `CREATE TABLE test (id INTEGER)`}}); err != nil {
		t.Fatal(err)
	}
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("commit did not trigger replication")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(350 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("unexpected polling", calls.Load())
	}
	cancel()
	<-done
}

func TestReplicatorSlowPeerDoesNotBlockHealthyPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := localForTest(t, "node-a")
	var healthy atomic.Int64
	r := &Replicator{DB: db, StateDir: t.TempDir(), Peers: func(context.Context) ([]ReplicaPeer, error) {
		return []ReplicaPeer{{NodeID: "slow"}, {NodeID: "healthy"}}, nil
	}, Transfer: func(ctx context.Context, p ReplicaPeer, path string) (LocalIdentity, error) {
		if p.NodeID == "slow" {
			<-ctx.Done()
			return LocalIdentity{}, ctx.Err()
		}
		healthy.Add(1)
		return db.LocalIdentity(ctx)
	}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for healthy.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("healthy peer not synchronized")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := db.Execute(ctx, []Statement{{SQL: `CREATE TABLE extra (id INTEGER)`}}); err != nil {
		t.Fatal(err)
	}
	for healthy.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("slow peer blocked healthy catchup")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestCacheRefreshPreservesTOTPReplayCounter(t *testing.T) {
	ctx := context.Background()
	source := localForTest(t, "node-a")
	target := localForTest(t, "node-b")
	for _, c := range []*Client{source, target} {
		if err := Migrate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Execute(ctx, []Statement{{SQL: `INSERT INTO user_credentials VALUES('alice','secret',1)`}}); err != nil {
		t.Fatal(err)
	}
	if err := target.Execute(ctx, []Statement{{SQL: `INSERT INTO user_credentials VALUES('alice','secret',99)`}}); err != nil {
		t.Fatal(err)
	}
	if err := CopyTables(ctx, source, target, []string{"user_credentials"}); err != nil {
		t.Fatal(err)
	}
	rows, err := target.Query(ctx, Statement{SQL: `SELECT last_totp_step FROM user_credentials`})
	if err != nil || rows.Values[0][0] != float64(99) {
		t.Fatalf("counter rewound: %v %v", rows, err)
	}
	if err := source.Execute(ctx, []Statement{{SQL: `UPDATE user_credentials SET totp_secret='rotated',last_totp_step=0`}}); err != nil {
		t.Fatal(err)
	}
	if err := CopyTables(ctx, source, target, []string{"user_credentials"}); err != nil {
		t.Fatal(err)
	}
	rows, err = target.Query(ctx, Statement{SQL: `SELECT last_totp_step FROM user_credentials`})
	if err != nil || rows.Values[0][0] != float64(0) {
		t.Fatalf("new secret kept old counter: %v %v", rows, err)
	}
}

func TestImportLocalMarkerCommitsWithRowsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	source := localForTest(t, "old")
	target := localForTest(t, "owner")
	for _, c := range []*Client{source, target} {
		if err := Migrate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Execute(ctx, []Statement{{SQL: `INSERT INTO scheduler_state(stack_id,tier) VALUES('stack','1h')`}}); err != nil {
		t.Fatal(err)
	}
	if err := ImportLocal(ctx, source, target, []string{"scheduler_state"}); err != nil {
		t.Fatal(err)
	}
	done, err := target.LocalMigrationDone(ctx, "legacy-rqlite")
	if err != nil || !done {
		t.Fatal("missing durable import marker", err)
	}
	if err := target.Execute(ctx, []Statement{{SQL: `DELETE FROM scheduler_state`}}); err != nil {
		t.Fatal(err)
	}
	if err := ImportLocal(ctx, source, target, []string{"scheduler_state"}); err != nil {
		t.Fatal(err)
	}
	rows, err := target.Query(ctx, Statement{SQL: `SELECT * FROM scheduler_state`})
	if err != nil || len(rows.Values) != 0 {
		t.Fatal("idempotent import rewound owner state", rows, err)
	}
}
