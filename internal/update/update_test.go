package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockExcludesOtherCoordinators(t *testing.T) {
	dir := t.TempDir()
	unlock, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := Acquire(dir); e == nil {
		other()
		t.Fatal("concurrent lock acquired")
	}
	unlock()
	other, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	other()
}
func TestDurablePrivateJournalReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if e := WriteJSON(path, map[string]string{"phase": "restoring"}); e != nil {
		t.Fatal(e)
	}
	if e := WriteJSON(path, map[string]string{"phase": "committed"}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "{\n  \"phase\": \"committed\"\n}\n" {
		t.Fatal(string(b))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestUpstreamRepository(t *testing.T) {
	for _, value := range []string{"", "floatlab-hq/floatlab-core", "my-org/images.dev"} {
		if err := ValidateUpstream(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"../repo", "owner/..", "https://github.com/owner/repo", "owner/repo/path", "owner/repo?x", "owner/repo#x", "owner/repo%2fother", "owner/ repo"} {
		if ValidateUpstream(value) == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if Repository("") != DefaultRepository || Repository("org/repo") != "org/repo" {
		t.Fatal("incorrect repository selection")
	}
}
