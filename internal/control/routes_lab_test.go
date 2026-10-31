package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestJoinDrainsWritesAndBlocksOldCacheLoginUntilSynced(t *testing.T) {
	s := &Server{}
	var pending atomic.Bool
	s.SetLabJoinGate(pending.Load)
	oldWrite, releaseOld, joined, releaseJoin := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	gate := s.labJoinFence(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/lab/invitations":
			close(oldWrite)
			<-releaseOld
		case "/api/v1/lab/join":
			pending.Store(true)
			close(joined)
			<-releaseJoin
		case "/api/v1/auth/token":
			t.Error("login reached stale cached users while new lab credentials were installed")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	oldDone := make(chan struct{})
	go func() {
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/lab/invitations", nil))
		close(oldDone)
	}()
	<-oldWrite
	joinDone := make(chan struct{})
	go func() {
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/lab/join", nil))
		close(joinDone)
	}()
	select {
	case <-joined:
		t.Fatal("join checked emptiness before the earlier mutation drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseOld)
	<-oldDone
	<-joined
	login := httptest.NewRecorder()
	loginDone := make(chan struct{})
	go func() {
		gate.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", nil))
		close(loginDone)
	}()
	select {
	case <-loginDone:
		t.Fatal("authentication was not drained by enrollment")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseJoin)
	<-joinDone
	<-loginDone
	if login.Code != http.StatusConflict || !strings.Contains(login.Body.String(), "lab_join_pending") {
		t.Fatalf("pending join allowed credential minting: %d %s", login.Code, login.Body.String())
	}
	read := httptest.NewRecorder()
	gate.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/v1/lab/status", nil))
	if read.Code != http.StatusNoContent {
		t.Fatal("pending join blocked status")
	}
	pending.Store(false)
	mutation := httptest.NewRecorder()
	gate.ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil))
	if mutation.Code != http.StatusNoContent {
		t.Fatal("synced enrollment remained fenced")
	}
}

func TestLabAdministrationRoutesRequireAdminAndReachLabHandler(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "test-secret", JWTIssuer: "floatlab", JWTAudience: "management"}, log: zap.NewNop(), auth: newAuthLimiter()}
	called := 0
	s.SetLabHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusNoContent) }))
	router := s.buildRouter()
	token := signedToken(t, "test-secret", []string{"admin"})
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/api/v1/lab/members/peer/removal"}, {http.MethodPost, "/api/v1/lab/identity/rotate"}, {http.MethodPost, "/api/v1/lab/members/peer/release-peer-address"}} {
		for _, authenticated := range []bool{false, true} {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			if authenticated {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			want := http.StatusUnauthorized
			if authenticated {
				want = http.StatusNoContent
			}
			if response.Code != want {
				t.Fatalf("%s %s auth=%v: %d %s", tc.method, tc.path, authenticated, response.Code, response.Body.String())
			}
		}
	}
	if called != 3 {
		t.Fatalf("unauthenticated request reached lab handler: %d calls", called)
	}
}

func TestLabModeRejectsLegacyNodeRowDeletion(t *testing.T) {
	s := &Server{cfg: &Config{}, labHandler: http.NotFoundHandler()}
	router := chi.NewRouter()
	registerNodeRoutes(router, s)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/nodes/peer", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "/removal") {
		t.Fatal(response.Code, response.Body.String())
	}
}
