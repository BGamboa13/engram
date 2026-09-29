package store

import (
	"errors"
	"sync"
	"testing"
)

func TestResumeSession(t *testing.T) {
	s := newTestStore(t)
	register := func(id, project string) string {
		t.Helper()
		effective, err := s.ResumeSessionWithOwnershipMode(id, project, "/work", SessionOwnershipProjectOwned)
		if err != nil {
			t.Fatal(err)
		}
		return effective
	}
	end := func(id string) {
		t.Helper()
		if err := s.EndSession(id, "done"); err != nil {
			t.Fatal(err)
		}
	}
	if got := register("root", "engram"); got != "root" {
		t.Fatal(got)
	}
	if got := register("root", "engram"); got != "root" {
		t.Fatal(got)
	}
	end("root")
	if err := s.StartSession("root", "engram", "/work"); !errors.Is(err, ErrSessionAlreadyEnded) {
		t.Fatalf("non-resume: %v", err)
	}
	if got := register("root", "engram"); got != "root:resume:2" {
		t.Fatal(got)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET runtime_lease_expires_at = datetime('now', '-1 hour') WHERE id = ?`, "root:resume:2"); err != nil {
		t.Fatal(err)
	}
	if got := register("root", "engram"); got != "root:resume:2" {
		t.Fatal(got)
	}
	var renewed bool
	if err := s.db.QueryRow(`SELECT runtime_lease_expires_at > datetime('now') FROM sessions WHERE id = ?`, "root:resume:2").Scan(&renewed); err != nil || !renewed {
		t.Fatalf("lease renewed=%v err=%v", renewed, err)
	}
	if _, err := s.ResumeSessionWithOwnershipMode("root", "foreign", "/work", SessionOwnershipProjectOwned); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("conflict: %v", err)
	}
	end("root:resume:2")
	if got := register("root", "engram"); got != "root:resume:3" {
		t.Fatal(got)
	}
	root, err := s.GetSession("root")
	if err != nil || root.EndedAt == nil {
		t.Fatalf("root reopened: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ResumeSessionWithOwnershipMode("root", "engram", "/work", SessionOwnershipProjectOwned); err != nil || got != "root:resume:3" {
		t.Fatalf("reopen: %q %v", got, err)
	}
}

func TestResumeSessionSuffixes(t *testing.T) {
	for _, root := range []string{"root", "root%_", `root\name`} {
		t.Run(root, func(t *testing.T) {
			s := newTestStore(t)
			for _, id := range []string{root, root + ":resume:8", root + ":resume:invalid", "rootOTHERx:resume:99", root + ":resume:9:child"} {
				if err := s.StartSession(id, "engram", "/work"); err != nil {
					t.Fatal(err)
				}
				if id != root+":resume:invalid" {
					if err := s.EndSession(id, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := s.ResumeSessionWithOwnershipMode(root, "engram", "/work", SessionOwnershipShared)
			if err != nil || got != root+":resume:9" {
				t.Fatalf("suffix: %q %v", got, err)
			}
		})
	}
}

func TestResumeSessionContinuationConflict(t *testing.T) {
	s := newTestStore(t)
	if err := s.StartSession("root", "engram", "/work"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession("root", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StartSessionWithOwnershipMode("root:resume:2", "foreign", "/work", SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	_, err := s.ResumeSessionWithOwnershipMode("root", "engram", "/work", SessionOwnershipShared)
	if !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := s.GetSession("root:resume:3"); err == nil {
		t.Fatal("advanced past conflict")
	}
}

func TestResumeSessionSelection(t *testing.T) {
	for _, tc := range []struct{ name, mode, candidateMode, candidateProject, suffix, want string }{
		{"shared cross-project compatible", SessionOwnershipShared, SessionOwnershipShared, "foreign", "2", "2"},
		{"strict same-project compatible", SessionOwnershipProjectOwned, SessionOwnershipShared, "engram", "2", "2"},
		{"numeric ordering", SessionOwnershipShared, SessionOwnershipShared, "engram", "10", "3"},
		{"no integer cap", SessionOwnershipShared, SessionOwnershipShared, "engram", "999999999999999999999999999999", "1000000000000000000000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			for _, id := range []string{"root", "root:resume:" + tc.suffix} {
				if err := s.StartSessionWithOwnershipMode(id, tc.candidateProject, "/work", tc.candidateMode); err != nil {
					t.Fatal(err)
				}
				if id == "root" || tc.name == "no integer cap" {
					if err := s.EndSession(id, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name == "numeric ordering" {
				if err := s.StartSession("root:resume:3", "engram", "/work"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.ResumeSessionWithOwnershipMode("root", "engram", "/work", tc.mode)
			if err != nil || got != "root:resume:"+tc.want {
				t.Fatalf("selection: %q %v", got, err)
			}
			session, err := s.GetSession(got)
			if err != nil || session.OwnershipMode != tc.candidateMode {
				t.Fatalf("persisted mode: %+v %v", session, err)
			}
		})
	}
}

func TestResumeSessionConcurrent(t *testing.T) {
	s := newTestStore(t)
	if err := s.StartSession("root", "engram", "/work"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession("root", ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ResumeSessionWithOwnershipMode("root", "engram", "/work", SessionOwnershipShared)
			if err != nil || got != "root:resume:2" {
				t.Errorf("concurrent: %q %v", got, err)
			}
		}()
	}
	wg.Wait()
}
