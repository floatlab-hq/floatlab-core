package config

import (
	"context"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"testing"
)

func TestManagementMigrationPreservesLegacyData(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `CREATE TABLE networks(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,prefix TEXT NOT NULL,reserved_min TEXT,reserved_max TEXT)`},
		{SQL: `INSERT INTO networks VALUES('network','legacy','fd00::/64','fd00::1','fd00::9')`},
		{SQL: `CREATE TABLE alert_rules(id TEXT PRIMARY KEY,name TEXT NOT NULL,description TEXT,type TEXT NOT NULL,condition TEXT NOT NULL,action TEXT,channel TEXT)`},
		{SQL: `INSERT INTO alert_rules VALUES('rule','legacy','keep me','metrics','up == 0','notify','email')`},
		{SQL: `INSERT INTO ip_reservations VALUES('reservation','stack','app','fd00::2/64','fd00::/64','2026-01-01T00:00:00Z')`},
		{SQL: `INSERT INTO notifications(id,kind,severity,title,body,state,created_at) VALUES('notification','alert','warning','title','body','unread','2026-01-01T00:00:00Z')`},
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		query string
		want  string
	}{
		{`SELECT reserved_min FROM networks WHERE id='network'`, "fd00::1"},
		{`SELECT description FROM alert_rules WHERE id='rule'`, "keep me"},
		{`SELECT prefix_pool FROM ip_reservations WHERE id='reservation'`, "network"},
		{`SELECT address FROM ip_reservations WHERE id='reservation'`, "fd00::2/64"},
		{`SELECT body FROM notifications WHERE id='notification'`, "body"},
	} {
		result, err := db.Query(ctx, rqlite.Statement{SQL: test.query})
		if err != nil || len(result.Values) != 1 || result.Values[0][0] != test.want {
			t.Fatalf("%s: result=%+v err=%v", test.query, result, err)
		}
	}
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT severity,enabled,annotations,created_at FROM alert_rules WHERE id='rule'`})
	if err != nil || len(result.Values) != 1 || result.Values[0][0] != "warning" || result.Values[0][1] != float64(1) || result.Values[0][2] != "{}" || result.Values[0][3] == "" {
		t.Fatalf("migration defaults: %+v %v", result, err)
	}
}
