# RFC: authenticated prompt inbox provenance before remote deletion

**Status: proposed end-to-end contract; session registration and pair-claim storage implemented, server admission not implemented.** This RFC defines the minimum non-cryptographic authority needed before #1464 can enter the merge queue. It does not change current sync behavior. The baseline is tracker `313f5269`; the review thread on #1464 records the hold. #1240 is separate.

## Decision in one minute

Local SQLite remains authoritative for local writes; cloud is a replication boundary. A cloud writer's authorization for prompt project `beta` alone cannot establish that its claimed `(session_id, source_inbox_id)` belongs to an `alpha` session. Neither accepted prompt history nor a cloud session index built from uploaded chunks is independent proof. Remote pair reservation requires an explicit, authenticated session registration and an immutable cross-project pair claim. A missing claim is pending/fail-loud, not an authoritative remote tombstone.

This is a server-enforced authorization contract, not a new inference rule based on `project = session.project`. Legitimate beta prompts under alpha sessions must remain possible.

## Authority and lifecycle (proposed surfaces, not existing routes)

| Step | Required authority and durable effect |
| --- | --- |
| Register session | An authenticated principal authorized for owner project `alpha` explicitly registers `session_id` as a globally unique session identity owned by `alpha`. Registration records the authorized principal/context and owner project independently of chunks and prompt mutations. An identical replay is idempotent; a different owner or incompatible registration for the same ID is a conflict, never a reassignment. The registry must not be populated from `cloud_project_sessions`, uploaded chunks, imported history, or a prompt upsert/delete. |
| Claim prompt pair | For a beta prompt referencing an alpha session, the claimant must hold authorization for **both** alpha and beta at claim time. Registration must already be verified. An authorized claim durably binds `(session_id, source_inbox_id)` to `(sync_id, beta)`; matching replay is idempotent and any competing sync ID or project is a conflict. Claims cannot bootstrap registration or be inferred from a beta upsert. Same-project claims still require session-owner and prompt-project authority; no special weaker bootstrap. |
| Apply verified delete | A delete may reserve or replay a remote pair only against the existing verified binding, with beta authorization for the mutation. It need not require renewed alpha authorization: alpha consent was checked at the original claim. Validate sync ID, session ID, inbox ID and beta project against that binding. A beta-only writer cannot create or change the claim, even by sending an upsert followed by a delete; an alpha-only writer cannot claim/delete beta. |

Registration and claim are explicit server admission operations/protocol surfaces to design, **not names of implemented endpoints**. Atomic uniqueness and conflict checks must survive concurrent retries. Authentication means server-verified principal and project grants, not fields supplied in the payload; no cryptographic offline capability is assumed. Revocation after a verified claim does not erase that historical binding, but current beta mutation authorization remains necessary. Idless legacy prompts remain pair-less and must not acquire a source inbox binding through inference.

## Unverified deletes and compatibility

- A delete without a proven binding, whether offline, delivered before registration/claim, imported, or from old history, must fail loudly or remain visibly pending. It must not create an authoritative remote tombstone, reserve a pair, or advance the **remote pull cursor as an authoritative delete**. Retrying after explicit authorization is allowed; transport and cursor handling must distinguish pending/unverified evidence from admitted mutations. Do not quietly acknowledge such a push as a successful delete.
- Preserve local-first deletion and its local tombstone. A local `POST /prompts` reusing a tombstoned pair continues to return 409 even after cloud reauthorization; reauthorization grants cloud provenance, not permission to reuse a deleted identity. Existing backup import skips prompts whose pair has a tombstone, and a backup file is not independent proof of pair ownership. Do not silently promote historical imported prompts or tombstones into verified cloud claims.
- An imported or offline alpha session needs explicit **source reauthorization under alpha** by a currently authorized principal before cloud registration, followed by dual alpha+beta authorization for a cross-project pair claim. A chunk, old cloud mutation, old project index, or pre-policy history cannot substitute. Existing keyed prompt claims and deletes lacking this evidence are unverified; attestation applies prospectively to the verified registration/claim, never retroactively proves an old event.
- Tradeoff: an offline prompt deleted before any successful sync cannot remotely reserve its inbox pair until an authorized pair claim is made. If the underlying prompt is no longer present, the client needs a deliberate, auditable claim of its retained local identity/tombstone, not a reconstructed cloud upsert. An imported backup cannot independently establish that authority. Keep unverified legacy state distinguishable from verified state in storage, export, diagnostics, and replay.
- Preserve existing visible sync policy: blocked sync is explicit rather than silently dropped; local tombstones survive retries; proven pair 409 replay protection, idless behavior, scoped project exports, and ordinary push/pull cursor and deferred/dead-letter semantics remain intact for admitted mutations. A protocol change must not strand unrelated admitted pull mutations behind an unverified historical record; quarantine/pending evidence is visible but never treated as an authoritative delete.

## Acceptance matrix for implementation slices

| Scenario | Expected outcome |
| --- | --- |
| Alpha-only, beta-only, dual grants | Alpha-only can register alpha but cannot claim/delete beta; beta-only cannot register or claim alpha, but may delete an already verified beta binding; dual grants can claim the registered cross-project pair. |
| Spoof upsert then delete | Beta-only upsert claiming alpha session/inbox cannot mint registration or pair proof; subsequent delete cannot reserve remotely, including when a chunk index contains the purported session. |
| Valid cross-project | Alpha registration, dual-authorized beta pair claim, then beta-authorized delete and restore/replay preserve the verified binding and scoped beta export. |
| Duplicate/conflict | Identical registration/claim/delete retry is idempotent; session owner collision, pair sync-ID/project collision, and mismatch on delete reject without changing binding, tombstone, or authoritative cursor. Concurrent claim retries have the same result. |
| Offline and out of order | Local tombstone persists; delete before registration/claim stays visibly pending without remote reservation or authoritative cursor advancement; explicit alpha reauthorization then dual claim permits retry. |
| Legacy and backup | Idless remains pair-less; pre-policy mutations/imported chunks remain unverified; backup import cannot authorize a claim, skips tombstoned prompt pairs locally, and later `POST /prompts` replay of that pair still yields 409 after reauthorization. |
| Existing policy | Blocked sync remains visible; proven tombstone replay and scoped exports work; admitted push/pull, deferred replay, and cursor progress retain their existing semantics. |

## Reviewable delivery sequence

Each PR is at most **400 authored diff lines**, includes its own tests/docs with behavior, and targets tracker #1464; no slice alone authorizes queueing. Slice 1 is this RFC only: **no behavior change**. Then separate bounded slices should establish (2) explicit authenticated session registration and persistence, (3) dual-authorized immutable pair claims and verified cloud mutation admission, and (4) local offline/import reauthorization, pending delete handling, cursor/export compatibility and end-to-end tests. Split a slice further if its end-to-end safety or line budget cannot fit; do not ship a server-only rejection before a viable client handshake. Validate exact integrated tracker head and review before reconsidering #1464. #1240 remains separate.

## Limitations

Remaining transport route names, pending retry UX, and any cryptographic offline issuer still require design and tests; none may turn chunk/import history into authority. Session registration and its storage schema exist. Cloud storage now supports explicit immutable prompt pair claims against registered sessions, without deriving claims from chunks or prompt history. `ClaimPromptPair` assumes its caller has authenticated the actor and checked both project grants; it does not perform authorization. No HTTP route, verified delete admission, or client handshake is implemented yet. The current cloud behavior does **not** enforce this RFC.
