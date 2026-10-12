package control

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestLoginIssuesAdminJWT(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("floatlab"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{
			"values": [][]interface{}{{string(hash), `["admin"]`}},
		}}})
	}))
	defer database.Close()

	s := &Server{cfg: &Config{JWTSecret: "secret", JWTIssuer: "floatlab", JWTAudience: "management"}, db: rqlite.NewClient(database.URL)}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", strings.NewReader(`{"username":"demo","password":"floatlab"}`))
	response := httptest.NewRecorder()
	s.handleLogin(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response must not be cached")
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	claims := &adminClaims{}
	token, err := jwt.ParseWithClaims(body.AccessToken, claims, func(*jwt.Token) (interface{}, error) { return []byte("secret"), nil }, jwt.WithIssuer("floatlab"), jwt.WithAudience("management"))
	if err != nil || !token.Valid || claims.Subject != "demo" || !contains(claims.Roles, "admin") {
		t.Fatalf("issued token = %#v, claims = %#v, error = %v", token, claims, err)
	}
}

func TestBootstrapUserStoresHash(t *testing.T) {
	var payload string
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		payload = string(body)
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{}}})
	}))
	defer database.Close()

	if err := BootstrapUser(context.Background(), rqlite.NewClient(database.URL), "demo", "floatlab"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"floatlab"`) || !strings.Contains(payload, `"$2`) {
		t.Fatalf("bootstrap payload does not contain only a bcrypt hash: %s", payload)
	}
}

func TestAuthRateLimit(t *testing.T) {
	s := &Server{auth: newAuthLimiter()}
	handler := s.limitAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for attempt := 1; attempt <= 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/auth/token", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func TestSecondFactorChallengeAndAtomicConsumption(t *testing.T) {
	key := make([]byte, 32)
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	encrypted, err := credentials.Encrypt(key, secret)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	var mu sync.Mutex
	last := int64(-1)
	recoveryAvailable := true
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var stmts [][]interface{}
		if err := json.NewDecoder(r.Body).Decode(&stmts); err != nil {
			t.Error(err)
			return
		}
		sql, _ := stmts[0][0].(string)
		if strings.Contains(sql, "SELECT u.password_hash") {
			writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{"values": [][]interface{}{{string(hash), `["admin"]`, encrypted, last}}}}})
			return
		}
		affected := 0
		if strings.HasPrefix(sql, "UPDATE user_credentials") {
			step := int64(stmts[0][1].(float64))
			if step > last {
				last = step
				affected = 1
			}
		}
		if strings.HasPrefix(sql, "DELETE FROM user_recovery_codes") && recoveryAvailable && stmts[0][2] == credentials.RecoveryHash("saved-code") {
			recoveryAvailable = false
			affected = 1
		}
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{"rows_affected": affected}}})
	}))
	defer database.Close()
	server := &Server{cfg: &Config{JWTSecret: "secret", CredentialKey: key}, db: rqlite.NewClient(database.URL)}
	login := func(factor map[string]string) *httptest.ResponseRecorder {
		request := map[string]string{"username": "admin", "password": "password"}
		for k, v := range factor {
			request[k] = v
		}
		body, _ := json.Marshal(request)
		result := httptest.NewRecorder()
		server.handleLogin(result, httptest.NewRequest("POST", "/auth/token", bytes.NewReader(body)))
		return result
	}
	challenge := login(nil)
	if challenge.Code != 401 || !strings.Contains(challenge.Body.String(), `"second_factor_required":true`) {
		t.Fatal(challenge.Code, challenge.Body.String())
	}
	bad := login(map[string]string{"totp_code": "abcdef"})
	if bad.Code != 401 {
		t.Fatal("invalid code accepted")
	}
	code, _ := credentials.Code(secret, time.Now().Unix()/30)
	if result := login(map[string]string{"totp_code": code}); result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	if result := login(map[string]string{"totp_code": code}); result.Code != 401 {
		t.Fatal("replayed TOTP accepted")
	}
	if result := login(map[string]string{"recovery_code": "saved-code"}); result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	if result := login(map[string]string{"recovery_code": "saved-code"}); result.Code != 401 {
		t.Fatal("replayed recovery code accepted")
	}
}

func TestBootstrapFileDoesNotResetExistingCredentials(t *testing.T) {
	root := t.TempDir()
	path := root + "/bootstrap.json"
	hash, _ := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	b := credentials.Bootstrap{Username: "admin", PasswordHash: string(hash), NodeID: "node", NodeName: "floatlab", TOTPSecret: "encrypted", RecoveryHashes: []string{"hash"}}
	var requests int
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var stmts [][]interface{}
		json.NewDecoder(r.Body).Decode(&stmts)
		requests++
		for _, stmt := range stmts {
			sql := stmt[0].(string)
			if strings.Contains(sql, "UPDATE") || (strings.Contains(sql, "user_credentials") || strings.Contains(sql, "user_recovery_codes")) && !strings.Contains(sql, "NOT EXISTS(SELECT 1 FROM users") {
				t.Error("bootstrap can reset an existing account:", sql)
			}
		}
		if !strings.Contains(stmts[len(stmts)-1][0].(string), "INSERT OR IGNORE INTO users") {
			t.Error("user must be inserted last")
		}
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{}}})
	}))
	defer database.Close()
	for i := 0; i < 2; i++ {
		if err := credentials.WritePrivate(path, b); err != nil {
			t.Fatal(err)
		}
		if err := BootstrapFile(context.Background(), rqlite.NewClient(database.URL), path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("bootstrap retained")
		}
	}
	if requests != 2 {
		t.Fatal(requests)
	}
	if err := BootstrapFile(context.Background(), rqlite.NewClient(database.URL), path); err != nil {
		t.Fatal("missing bootstrap is not idempotent", err)
	}
}

func TestBootstrapAndRecoveryAgainstSQLite(t *testing.T) {
	ctx := context.Background()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	secret, _ := credentials.Secret()
	encrypted, _ := credentials.Encrypt(key, secret)
	hash, _ := bcrypt.GenerateFromPassword([]byte("original"), bcrypt.MinCost)
	b := credentials.Bootstrap{Username: "admin", PasswordHash: string(hash), NodeID: "node", NodeName: "floatlab", TOTPSecret: encrypted, RecoveryHashes: []string{credentials.RecoveryHash("one-use")}}
	path := t.TempDir() + "/bootstrap.json"
	if err := credentials.WritePrivate(path, b); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapFile(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: &Config{CredentialKey: key}, db: db}
	request := loginRequest{Username: "admin", RecoveryCode: "one-use"}
	if ok, err := server.verifySecondFactor(ctx, request, encrypted, float64(-1)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// A repeated file must not resurrect the used recovery code or replace the password/factor.
	newHash, _ := bcrypt.GenerateFromPassword([]byte("replacement"), bcrypt.MinCost)
	b.PasswordHash = string(newHash)
	b.TOTPSecret = "replacement"
	if err := credentials.WritePrivate(path, b); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapFile(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	if ok, err := server.verifySecondFactor(ctx, request, encrypted, float64(-1)); err != nil || ok {
		t.Fatal("recovery code resurrected", ok, err)
	}
	result, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT u.password_hash,c.totp_secret FROM users u JOIN user_credentials c USING(username) WHERE u.username='admin'`})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Values) != 1 || result.Values[0][0] != string(hash) || result.Values[0][1] != encrypted {
		t.Fatal("existing credentials reset", result)
	}
}
