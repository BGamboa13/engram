package store

import "testing"

func TestLocalPromptCreationIdentity(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	check := func(syncID, session, inbox, project string, eligible bool) {
		t.Helper()
		gotSession, gotInbox, gotProject, gotEligible, err := s.LocalPromptCreationIdentity(syncID)
		if err != nil || gotSession != session || gotInbox != inbox || gotProject != project || gotEligible != eligible {
			t.Fatalf("syncID %q: %q %q %q %v %v", syncID, gotSession, gotInbox, gotProject, gotEligible, err)
		}
	}
	check("", "", "", "", false)
	keyed := AddPromptParams{SessionID: "session", Project: "beta", SourceInboxID: "inbox", Content: "local"}
	id, inserted, err := s.AddPromptWithResult(keyed)
	if err != nil || !inserted {
		t.Fatalf("insert: %v %v", inserted, err)
	}
	var localSyncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, id).Scan(&localSyncID); err != nil {
		t.Fatal(err)
	}
	check(localSyncID, "session", "inbox", "beta", true)
	replay, inserted, err := s.AddPromptWithResult(keyed)
	if err != nil || inserted || replay != id {
		t.Fatalf("replay: %d %v %v", replay, inserted, err)
	}
	check(localSyncID, "session", "inbox", "beta", true)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 1, Entity: SyncEntityPrompt, EntityKey: "remote", Op: SyncOpUpsert, Payload: `{"sync_id":"remote","session_id":"session","source_inbox_id":"remote-key","project":"beta","content":"remote"}`, Source: SyncSourceRemote, Project: "beta"}); err != nil {
		t.Fatal(err)
	}
	check("remote", "", "", "", false)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 2, Entity: SyncEntityPrompt, EntityKey: "remote", Op: SyncOpUpsert, Payload: `{"sync_id":"remote","session_id":"session","source_inbox_id":"remote-key","project":"beta","content":"changed"}`, Source: SyncSourceRemote, Project: "beta"}); err != nil {
		t.Fatal(err)
	}
	check("remote", "", "", "", false)
	if _, inserted, err := s.AddPromptWithResult(AddPromptParams{SessionID: "session", Project: "beta", SourceInboxID: "remote-key", Content: "collision"}); err != nil || inserted {
		t.Fatalf("collision: %v %v", inserted, err)
	}
	check("remote", "", "", "", false)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 3, Entity: SyncEntityPrompt, EntityKey: "different", Op: SyncOpUpsert, Payload: `{"sync_id":"different","session_id":"session","source_inbox_id":"inbox","project":"beta","content":"collision"}`, Source: SyncSourceRemote, Project: "beta"}); err == nil {
		t.Fatal("pulled collision accepted")
	}
	check(localSyncID, "session", "inbox", "beta", true)
	backup, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	imported := newTestStore(t)
	if _, err := imported.Import(backup); err != nil {
		t.Fatal(err)
	}
	session, inbox, project, eligible, err := imported.LocalPromptCreationIdentity(localSyncID)
	if err != nil || session != "" || inbox != "" || project != "" || eligible {
		t.Fatalf("import promoted identity: %q %q %q %v %v", session, inbox, project, eligible, err)
	}
	plain, inserted, err := s.AddPromptIfMissing(AddPromptParams{SessionID: "session", Project: "beta", Content: "plain"})
	if err != nil || !inserted {
		t.Fatalf("idless: %v %v", inserted, err)
	}
	var plainSyncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, plain).Scan(&plainSyncID); err != nil {
		t.Fatal(err)
	}
	check(plainSyncID, "", "", "", false)
	idless, inserted, err := s.AddPromptWithResult(AddPromptParams{SessionID: "session", Project: "beta", Content: "another idless"})
	if err != nil || !inserted {
		t.Fatalf("idless AddPromptWithResult: %v %v", inserted, err)
	}
	var idlessSyncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, idless).Scan(&idlessSyncID); err != nil {
		t.Fatal(err)
	}
	check(idlessSyncID, "", "", "", false)
	check("missing", "", "", "", false)
	if _, err := s.DB().Exec(`UPDATE user_prompts SET project='gamma' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	check(localSyncID, "", "", "", false)
	if _, err := s.DB().Exec(`UPDATE user_prompts SET project='beta', source_inbox_id='other' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	check(localSyncID, "", "", "", false)
	if err := s.CreateSession("other", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE user_prompts SET source_inbox_id='inbox', session_id='other' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	check(localSyncID, "", "", "", false)
	deleteID, inserted, err := s.AddPromptWithResult(AddPromptParams{SessionID: "session", Project: "beta", SourceInboxID: "delete-inbox", Content: "delete"})
	if err != nil || !inserted {
		t.Fatalf("delete insert: %v %v", inserted, err)
	}
	var deleteSyncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, deleteID).Scan(&deleteSyncID); err != nil {
		t.Fatal(err)
	}
	check(deleteSyncID, "session", "delete-inbox", "beta", true)
	if err := s.DeletePrompt(deleteID); err != nil {
		t.Fatal(err)
	}
	check(deleteSyncID, "session", "delete-inbox", "beta", true)
	var origin string
	if err := s.DB().QueryRow(`SELECT local_creation_project FROM prompt_tombstones WHERE sync_id=?`, deleteSyncID).Scan(&origin); err != nil || origin != "beta" {
		t.Fatalf("local tombstone origin: %q %v", origin, err)
	}
}

func TestLocalPromptOriginSessionDeleteAndUntrustedTombstones(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("alpha-session", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.AddPromptWithResult(AddPromptParams{SessionID: "alpha-session", Project: "beta", SourceInboxID: "beta-inbox", Content: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var syncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, id).Scan(&syncID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("alpha-session"); err != nil {
		t.Fatal(err)
	}
	if session, inbox, project, ok, err := s.LocalPromptCreationIdentity(syncID); err != nil || !ok || session != "alpha-session" || inbox != "beta-inbox" || project != "beta" {
		t.Fatalf("session delete: %q %q %q %v %v", session, inbox, project, ok, err)
	}
	checkUnknown := func(key string) {
		t.Helper()
		if a, b, c, ok, err := s.LocalPromptCreationIdentity(key); err != nil || ok || a != "" || b != "" || c != "" {
			t.Fatalf("untrusted %s: %q %q %q %v %v", key, a, b, c, ok, err)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO prompt_tombstones(sync_id,session_id,source_inbox_id,project) VALUES ('imported','alpha-session','import-key','beta')`); err != nil {
		t.Fatal(err)
	}
	checkUnknown("imported")
	backup, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	imported := newTestStore(t)
	if _, err := imported.Import(backup); err != nil {
		t.Fatal(err)
	}
	if a, b, c, ok, err := imported.LocalPromptCreationIdentity(syncID); err != nil || ok || a != "" || b != "" || c != "" {
		t.Fatalf("imported tombstone promoted: %q %q %q %v %v", a, b, c, ok, err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 11, Entity: SyncEntityPrompt, EntityKey: "pulled", Op: SyncOpDelete, Payload: `{"sync_id":"pulled","session_id":"alpha-session","source_inbox_id":"pull-key","project":"beta","deleted":true,"hard_delete":true}`, Source: SyncSourceRemote, Project: "beta"}); err != nil {
		t.Fatal(err)
	}
	checkUnknown("pulled")
	if _, err := s.DB().Exec(`UPDATE prompt_tombstones SET project='gamma' WHERE sync_id=?`, syncID); err != nil {
		t.Fatal(err)
	}
	checkUnknown(syncID)
	if _, err := s.DB().Exec(`UPDATE prompt_tombstones SET project='beta' WHERE sync_id=?`, syncID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("other", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES (?,'other','duplicate','beta','duplicate')`, syncID); err != nil {
		t.Fatal(err)
	}
	checkUnknown(syncID)
}

func TestLocalPromptOriginDeleteBoundaries(t *testing.T) {
	checkUnknown := func(t *testing.T, s *Store, key string) {
		t.Helper()
		a, b, c, ok, err := s.LocalPromptCreationIdentity(key)
		if err != nil || ok || a != "" || b != "" || c != "" {
			t.Fatalf("unexpected origin: %q %q %q %v %v", a, b, c, ok, err)
		}
	}
	newLocal := func(t *testing.T, s *Store, inbox string) (int64, string) {
		t.Helper()
		id, _, err := s.AddPromptWithResult(AddPromptParams{SessionID: "alpha-session", Project: "beta", SourceInboxID: inbox, Content: "local"})
		if err != nil {
			t.Fatal(err)
		}
		var key string
		if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, id).Scan(&key); err != nil {
			t.Fatal(err)
		}
		return id, key
	}
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *Store)
	}{
		{"existing remote tombstone", func(t *testing.T, s *Store) {
			id, key := newLocal(t, s, "remote-inbox")
			if _, err := s.DB().Exec(`INSERT INTO prompt_tombstones(sync_id,session_id,source_inbox_id,project) VALUES (?,'alpha-session','remote-inbox','beta')`, key); err != nil {
				t.Fatal(err)
			}
			if err := s.DeletePrompt(id); err != nil {
				t.Fatal(err)
			}
			checkUnknown(t, s, key)
		}},
		{"pulled delete of live local prompt", func(t *testing.T, s *Store) {
			_, key := newLocal(t, s, "pulled-inbox")
			err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 21, Entity: SyncEntityPrompt, EntityKey: key, Op: SyncOpDelete, Payload: `{"sync_id":"` + key + `","session_id":"alpha-session","source_inbox_id":"pulled-inbox","project":"beta","deleted":true,"hard_delete":true}`, Source: SyncSourceRemote, Project: "beta"})
			if err != nil {
				t.Fatal(err)
			}
			checkUnknown(t, s, key)
		}},
		{"duplicate sync ID at deletion", func(t *testing.T, s *Store) {
			id, key := newLocal(t, s, "first-inbox")
			if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES (?,'alpha-session','second-inbox','beta','duplicate')`, key); err != nil {
				t.Fatal(err)
			}
			if err := s.DeletePrompt(id); err != nil {
				t.Fatal(err)
			}
			checkUnknown(t, s, key)
		}},
		{"replay retains origin and conflict fails closed", func(t *testing.T, s *Store) {
			id, key := newLocal(t, s, "replay-inbox")
			if err := s.DeletePrompt(id); err != nil {
				t.Fatal(err)
			}
			tx, err := s.DB().Begin()
			if err != nil {
				t.Fatal(err)
			}
			project := "beta"
			if err := s.recordPromptTombstoneTx(tx, key, "alpha-session", &project, "replay-inbox", Now()); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			a, b, c, ok, err := s.LocalPromptCreationIdentity(key)
			if err != nil || !ok || a != "alpha-session" || b != "replay-inbox" || c != "beta" {
				t.Fatalf("replay origin: %q %q %q %v %v", a, b, c, ok, err)
			}
			tx, err = s.DB().Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.recordPromptTombstoneTx(tx, key, "alpha-session", &project, "conflicting-inbox", Now()); err == nil {
				_ = tx.Rollback()
				t.Fatal("conflicting replay accepted")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("alpha-session", "alpha", "/work"); err != nil {
				t.Fatal(err)
			}
			tc.run(t, s)
		})
	}
}

func TestLocalPromptCreationIdentityDuplicateSyncID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.AddPromptWithResult(AddPromptParams{SessionID: "session", Project: "beta", SourceInboxID: "key", Content: "first"})
	if err != nil {
		t.Fatal(err)
	}
	var syncID string
	if err := s.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id=?`, id).Scan(&syncID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES (?,'session','duplicate','beta','second')`, syncID); err != nil {
		t.Fatal(err)
	}
	session, inbox, project, eligible, err := s.LocalPromptCreationIdentity(syncID)
	if err != nil || session != "" || inbox != "" || project != "" || eligible {
		t.Fatalf("duplicate promoted identity: %q %q %q %v %v", session, inbox, project, eligible, err)
	}
}

func TestLocalPromptCreationIdentityMigration(t *testing.T) {
	cfg := FallbackConfig(t.TempDir())
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('old','alpha','/work')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES ('old-prompt','old','key','beta','old')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO prompt_tombstones(sync_id,session_id,source_inbox_id,project) VALUES ('old-deleted','old','key','beta')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`ALTER TABLE prompt_tombstones DROP COLUMN local_creation_session_id`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`ALTER TABLE user_prompts DROP COLUMN local_creation_session_id`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = s.LocalPromptCreationIdentity("old-prompt"); err == nil {
		t.Fatal("missing column should fail closed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, key := range []string{"old-prompt", "old-deleted"} {
		session, inbox, project, eligible, err := s.LocalPromptCreationIdentity(key)
		if err != nil || session != "" || inbox != "" || project != "" || eligible {
			t.Fatalf("migration %s: %q %q %q %v %v", key, session, inbox, project, eligible, err)
		}
	}
}
