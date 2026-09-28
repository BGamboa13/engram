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
	session, inbox, project, eligible, err := s.LocalPromptCreationIdentity("old-prompt")
	if err != nil || session != "" || inbox != "" || project != "" || eligible {
		t.Fatalf("migration: %q %q %q %v %v", session, inbox, project, eligible, err)
	}
}
