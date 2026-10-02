package store

import (
	"path/filepath"
	"testing"
)

func TestStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inari", "sessions.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("discord:1", Binding{SessionID: "ses_a", CWD: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("discord:2", Binding{SessionID: "ses_b", CWD: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("discord:2"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := s.Get("discord:1"); !ok || b.SessionID != "ses_a" {
		t.Fatalf("Get = %+v, %v", b, ok)
	}
	if _, ok := s.Get("discord:2"); ok {
		t.Fatal("deleted binding came back")
	}
}
