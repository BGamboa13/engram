package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrPromptSourceConfirmation = errors.New("prompt source confirmation rejected")

// LookupPromptSourceConfirmation reads only an explicit confirmation for this remote target.
// It rechecks the current prompt tuple and rejects contradictory session ownership;
// ordinary prompt or session metadata never creates a confirmation.
func (s *Store) LookupPromptSourceConfirmation(remoteTarget, syncID string) (PromptSourcePreview, string, int64, bool, error) {
	var preview PromptSourcePreview
	if strings.TrimSpace(remoteTarget) == "" || strings.TrimSpace(syncID) == "" {
		return preview, "", 0, false, ErrPromptSourceConfirmation
	}
	var owner string
	var auditID int64
	err := s.db.QueryRow(`SELECT session_id,source_inbox_id,prompt_project,sync_id,kind,asserted_owner_project,remote_attestation_id
  FROM prompt_source_confirmations WHERE remote_target=? AND sync_id=?`, remoteTarget, syncID).
		Scan(&preview.SessionID, &preview.SourceInboxID, &preview.Project, &preview.SyncID, &preview.Kind, &owner, &auditID)
	if errors.Is(err, sql.ErrNoRows) {
		return PromptSourcePreview{}, "", 0, false, nil
	}
	if err != nil {
		return PromptSourcePreview{}, "", 0, false, err
	}
	reject := func() (PromptSourcePreview, string, int64, bool, error) {
		return PromptSourcePreview{}, "", 0, false, ErrPromptSourceConfirmation
	}
	if strings.TrimSpace(preview.SessionID) == "" || strings.TrimSpace(preview.SourceInboxID) == "" || strings.TrimSpace(preview.Project) == "" || strings.TrimSpace(owner) == "" || auditID <= 0 || preview.SyncID != syncID || (preview.Kind != "live" && preview.Kind != "deleted") {
		return reject()
	}
	rows, err := s.db.Query(`SELECT ifnull(session_id,''),ifnull(source_inbox_id,''),ifnull(project,''),ifnull(sync_id,''),'live' FROM user_prompts WHERE sync_id=?
 UNION ALL SELECT ifnull(session_id,''),ifnull(source_inbox_id,''),ifnull(project,''),ifnull(sync_id,''),'deleted' FROM prompt_tombstones WHERE sync_id=? LIMIT 2`, syncID, syncID)
	if err != nil {
		return PromptSourcePreview{}, "", 0, false, err
	}
	defer func() { _ = rows.Close() }()
	var current PromptSourcePreview
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return PromptSourcePreview{}, "", 0, false, err
		}
		return reject()
	}
	if err = rows.Scan(&current.SessionID, &current.SourceInboxID, &current.Project, &current.SyncID, &current.Kind); err != nil {
		return PromptSourcePreview{}, "", 0, false, err
	}
	ambiguous := rows.Next()
	if err = rows.Err(); err != nil {
		return PromptSourcePreview{}, "", 0, false, err
	}
	if ambiguous || current != preview {
		return reject()
	}
	if err = rows.Close(); err != nil {
		return PromptSourcePreview{}, "", 0, false, err
	}
	var sessionOwner string
	err = s.db.QueryRow(`SELECT project FROM sessions WHERE id=?`, preview.SessionID).Scan(&sessionOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PromptSourcePreview{}, "", 0, false, err
	}
	if err == nil && strings.TrimSpace(sessionOwner) != "" && sessionOwner != owner {
		return reject()
	}
	return preview, owner, auditID, true, nil
}

// ConfirmPromptSourceAttestation records an explicit owner assertion only after
// the caller reports remote attestation success. The observed tuple is not
// ownership proof. A tuple recheck cannot detect change-away-and-back without
// a revision; autosync must revalidate cloud authorization before using it.
// Newer IDs are safe only within the same remote target while it retains the
// same Postgres identity sequence. Callers must supply an endpoint identity
// independent of local sync TargetKey and recheck cloud state before use.
func (s *Store) ConfirmPromptSourceAttestation(remoteTarget string, preview PromptSourcePreview, assertedOwner string, remoteAttestationID int64) error {
	if strings.TrimSpace(remoteTarget) == "" || strings.TrimSpace(assertedOwner) == "" || remoteAttestationID <= 0 || strings.TrimSpace(preview.SyncID) == "" ||
		strings.TrimSpace(preview.SessionID) == "" || strings.TrimSpace(preview.SourceInboxID) == "" ||
		strings.TrimSpace(preview.Project) == "" || (preview.Kind != "live" && preview.Kind != "deleted") {
		return ErrPromptSourceConfirmation
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT ifnull(session_id,''),ifnull(source_inbox_id,''),ifnull(project,''),ifnull(sync_id,''),'live'
  FROM user_prompts WHERE sync_id=?
  UNION ALL
  SELECT ifnull(session_id,''),ifnull(source_inbox_id,''),ifnull(project,''),ifnull(sync_id,''),'deleted'
  FROM prompt_tombstones WHERE sync_id=? LIMIT 2`, preview.SyncID, preview.SyncID)
	if err != nil {
		return err
	}
	var current PromptSourcePreview
	if !rows.Next() {
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		return ErrPromptSourceConfirmation
	}
	if err = rows.Scan(&current.SessionID, &current.SourceInboxID, &current.Project, &current.SyncID, &current.Kind); err != nil {
		_ = rows.Close()
		return err
	}
	ambiguous := rows.Next()
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if ambiguous || current != preview || strings.TrimSpace(current.SessionID) == "" || strings.TrimSpace(current.SourceInboxID) == "" || strings.TrimSpace(current.Project) == "" {
		return ErrPromptSourceConfirmation
	}
	// A live session can contradict an assertion, but absence is not evidence
	// against a deleted prompt. Neither session.project nor tombstones grant ownership.
	var observedOwner string
	err = tx.QueryRow(`SELECT project FROM sessions WHERE id=?`, preview.SessionID).Scan(&observedOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && strings.TrimSpace(observedOwner) != "" && observedOwner != assertedOwner {
		return ErrPromptSourceConfirmation
	}
	var existing PromptSourcePreview
	var owner string
	var attestationID int64
	err = tx.QueryRow(`SELECT session_id,source_inbox_id,prompt_project,sync_id,kind,asserted_owner_project,remote_attestation_id
  FROM prompt_source_confirmations WHERE remote_target=? AND sync_id=?`, remoteTarget, preview.SyncID).
		Scan(&existing.SessionID, &existing.SourceInboxID, &existing.Project, &existing.SyncID, &existing.Kind, &owner, &attestationID)
	if err == nil {
		if existing.SessionID != preview.SessionID || existing.SourceInboxID != preview.SourceInboxID ||
			existing.Project != preview.Project || existing.SyncID != preview.SyncID || owner != assertedOwner {
			return ErrPromptSourceConfirmation
		}
		if remoteAttestationID <= attestationID {
			if remoteAttestationID == attestationID && existing.Kind == preview.Kind {
				return tx.Commit()
			}
			return ErrPromptSourceConfirmation
		}
		if existing.Kind != "live" && existing.Kind != "deleted" {
			return ErrPromptSourceConfirmation
		}
		if _, err := tx.Exec(`UPDATE prompt_source_confirmations SET kind=?,remote_attestation_id=? WHERE remote_target=? AND sync_id=?`,
			preview.Kind, remoteAttestationID, remoteTarget, preview.SyncID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO prompt_source_confirmations
  (remote_target,sync_id,session_id,source_inbox_id,prompt_project,kind,asserted_owner_project,remote_attestation_id)
  VALUES (?,?,?,?,?,?,?,?)`, remoteTarget, preview.SyncID, preview.SessionID, preview.SourceInboxID, preview.Project, preview.Kind, assertedOwner, remoteAttestationID)
	if err != nil {
		return fmt.Errorf("insert prompt source confirmation: %w", err)
	}
	return tx.Commit()
}
