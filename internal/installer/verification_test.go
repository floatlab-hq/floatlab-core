package installer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/golang-jwt/jwt/v5"
)

func TestHashOnlyVerificationChecksInstalledAccountBeforeIssuingReadinessToken(t *testing.T) {
	sub := Submission{Host: Appliance{JWTSecret: "unique-installed-signing-key"}, Admin: credentials.Bootstrap{Username: "admin", PasswordHash: "expected-hash"}}
	for _, scenario := range []string{"matching", "wrong-hash", "missing", "wrong-role", "unavailable", "lab-matching", "lab-wrong-hash", "lab-wrong-role", "lab-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			sub := sub
			sub.Host.EnableLab = strings.HasPrefix(scenario, "lab-")
			sub.Host.Network.IPv4.Address = "192.0.2.10"
			scenario = strings.TrimPrefix(scenario, "lab-")
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$VERIFY_ARGS\"\ncat \"$VERIFY_RESPONSE\"\nif [ \"$VERIFY_SCENARIO\" = unavailable ]; then exit 1; fi\n"
			if err := os.WriteFile(filepath.Join(dir, "chroot"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			hash, roles := sub.Admin.PasswordHash, `["admin"]`
			if scenario == "wrong-hash" {
				hash = "different-hash"
			}
			if scenario == "wrong-role" {
				roles = `["viewer"]`
			}
			values := [][]any{{hash, roles}}
			if scenario == "missing" {
				values = nil
			}
			response, err := json.Marshal(map[string]any{"results": []any{map[string]any{"values": values}}})
			if err != nil {
				t.Fatal(err)
			}
			responsePath, argsPath := filepath.Join(dir, "response"), filepath.Join(dir, "args")
			if err := os.WriteFile(responsePath, response, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("VERIFY_ARGS", argsPath)
			t.Setenv("VERIFY_RESPONSE", responsePath)
			t.Setenv("VERIFY_SCENARIO", scenario)
			token, err := verifyHashedAccount(context.Background(), sub)
			args, readErr := os.ReadFile(argsPath)
			if sub.Host.EnableLab {
				for _, required := range []string{Runtime + "/root\n/run/current-system/sw/bin/curl\n", "--max-time\n10", "--cacert\n" + Persistent + "/lab/ca.pem", "--cert\n" + Persistent + "/lab/node.pem", "--key\n" + Persistent + "/lab/node-key.pem", "https://192.0.2.10:4001/db/query?level=weak", `[["SELECT password_hash,roles FROM users WHERE username=?","admin"]]`} {
					if readErr != nil || !strings.Contains(string(args), required) {
						t.Fatalf("lab verification omitted configured mTLS query argument %q: %v", required, readErr)
					}
				}
				if strings.Contains(string(args), "--insecure") || strings.Contains(string(args), "--location") {
					t.Fatal("lab account verification must pin the local rqlite trust and endpoint")
				}
			} else if readErr != nil || !strings.Contains(string(args), Runtime+"/root\n/run/current-system/sw/bin/docker\ncompose\n") || !strings.Contains(string(args), "exec\n-T\nrqlite\nwget\n") || !strings.Contains(string(args), `--post-data=[["SELECT password_hash,roles FROM users WHERE username=?","admin"]]`) {
				t.Fatal("verification did not query the account through the installed private runtime", readErr)
			}
			if scenario != "matching" {
				if err == nil || token != "" {
					t.Fatal("verification accepted an unverified administrator")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte(sub.Host.JWTSecret), nil }, jwt.WithValidMethods([]string{"HS256"}))
			if err != nil || !parsed.Valid {
				t.Fatal("invalid local readiness token", err)
			}
			subject, _ := parsed.Claims.GetSubject()
			expiry, _ := parsed.Claims.GetExpirationTime()
			if subject != "admin" || expiry == nil || time.Until(expiry.Time) > time.Minute {
				t.Fatal("readiness token has the wrong identity or lifetime")
			}
		})
	}
}
