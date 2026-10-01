# Feature: pi-cross-project-saves

Locator: `odd/tasks/pi-cross-project-saves.md` — Engram mirror topic `odd/pi-cross-project-saves/tasks` (project `engram`).
Branch: `fix/pi-cross-project-saves`.

## Objective

A Pi session bound to project A can save memories into an explicitly targeted project B (another repo/worktree) without a `session_project_conflict`, while every session keeps single-project ownership.

## Problem

`mem_save` with `project: "engram"` from a session owned by `gentle-pi` calls `registeredSessionForWrite(activeProject)` (`plugin/pi/index.ts` ~1606), which re-registers the SAME runtime session id as `project_owned` under the new project (~1097). The client guard `sessionProjectConflict` (~1011) or the server (409 `session_project_conflict`, `internal/server/server.go` ~548) rejects it, so the memory ends up in the wrong project.

## Why

`observations.project` is already independent of `sessions.project`. The `project_owned` invariant is intentional (prevented gentle-shell/gentle-pi contamination) and must stay.

## Decision (user, 2026-10-01)

Option 1 — explicit routing only. Target project comes from an explicit `project` or `cwd` argument. NO automatic inference from edited files: working in gentle-ai while patching gentle-pi must not make gentle-pi the default; only explicitly targeted saves go there.

## Scope

- Pi plugin: when the write target project differs from the runtime session's project, register and use a derived, stable, `project_owned` satellite session (`<runtimeId>@<project>`, no directory) and write with it.
- `cwd` argument on Pi write tools (at least `mem_save`), resolved through the same project resolution as `/project/current`.
- Tests and docs for the above.

## Non-goals

- No server ownership-rule relaxation; no switching Pi sessions to `shared`.
- No auto-inference from touched paths.

## Tasks

- [ ] T1 — Satellite session routing for explicit foreign `project` on Pi write tools (mem_save, mem_save_prompt, mem_session_summary; mem_capture_passive takes no project and is unchanged), with RED→GREEN tests. Route: delegated (writer; index.ts + tests). Commit: pending (parent-owned).
- [ ] T2 — `cwd` argument resolving the target project via `/project/current`, tests, and README/DOCS update. Route: delegated (writer). Commit: pending (parent-owned).

- [ ] T3 — After T1/T2 closure, investigate and fix `TestUnixSocketServesHTTPWithRestrictivePermissions` and `TestUnixSocketCloseIsIdempotent`, which also fail on main with an untrusted-writable socket parent hierarchy. Route: delegated exploration/writer/verification (security-sensitive filesystem contract). Preserve the trust validation; do not bypass it to make tests pass. User authorized this follow-up. Commit: pending.

## Acceptance criteria

- Session owned by A, `mem_save(project: B)` → observation stored in B under a B-owned satellite session; no 409; runtime session still owned by A.
- Same-project saves unchanged (same session id).
- Satellite id is stable/idempotent across repeated saves.
- `mem_save(cwd: <repo B path>)` routes to B.

## Checks

- `cd plugin/pi && npm test`
- `go test ./internal/server/... ./internal/store/...` if server touched.

## Delivery

Forecast ~250–400 authored lines. Strategy: exception-ok (user selected one PR). Push/PR are user decisions (issue-first per CONTRIBUTING.md).

## Progress

- 2026-10-01: exploration done, decision recorded (engram obs 21106), branch created.
- 2026-10-01: T1+T2 implemented by delegated writer; commits left to the parent (writer role forbids git add/commit).
- 2026-10-01: Corrected verifier P2/P3 with user-authorized server scope: POST /sessions preserves omitted/empty/whitespace directories as empty, while non-empty normalization is unchanged. Pi satellites omit directory; cwd only resolves the target project. Replaced the old omitted-directory normalization expectation and added runtime-candidate checks for both create/resume, plus project/cwd satellite wire assertions. Previously persisted nonblank directories are not cleared by renewal.

## Implementation notes

- Server only rejects blank session IDs (`internal/store/store.go` `validateSessionID`), so `@` is valid. The final isolated-registration contract preserves empty satellite directories; ordinary runtime registration retains its original server-cwd/worktree normalization. This supersedes the earlier blanket blank-directory handler correction.
- Runtime owner = local registered/in-flight owner, else persisted pending owner (ownerless stays unresolved → fail closed), else resolved detected project. Unknown owner keeps the legacy path (explicit project claims the runtime session).
- Satellites: `<runtimeID>@<project>`, `project_owned`, `resume: true` (ended satellites adopt `:resume:N`), renewed on every write (30-minute local lease), concurrent writes coalesce, ended on quit (not on reload).
- Only explicit `project`/`cwd` routes to satellites; implicit tool writes and prompt/passive hooks keep the runtime session.
- `cwd` + `project` disagreement (case-insensitive) and unresolved/ambiguous `cwd` fail before any registration.
- Existing tests that encoded "explicit foreign project fails" were rewritten to the new contract; two continuation-race tests now use ambiguous detection to keep exercising the legacy runtime-claim path.

## Verification evidence

- RED T1: 7/8 new tests failed with `Pi runtime session runtime-a belongs to Engram project project-a, not project-b` (same-project test passed).
- GREEN T1: `npm test` 248/248 after rewriting 8 legacy expectations.
- RED T2: 4/5 new cwd tests failed (cwd ignored). GREEN T2: `npm test` 253/253.
- Risk tier: medium-high (session ownership contract); independent verification recommended.
- P2/P3 correction RED: Go omitted/empty/whitespace cases failed in both create/resume modes (stored server cwd and appeared as runtime candidates); Pi project/cwd satellite wire assertions failed (251/253 passed).
- P2/P3 correction GREEN: focused Go directory regression passed (10 subcases); Pi npm test passed 253/253; go vet passed. Required Go package run still fails two unrelated Unix-socket tests (`socket parent hierarchy is writable by an untrusted user`); store passes. `gofmt -l internal` lists only untouched `internal/diagnostic/checks_test.go`, `internal/store/session_identity_repair_test.go`, and `internal/store/sync_apply_test.go`.

## Compatibility correction (implementation ready; closure pending)

- User authorized additional server/plugin/store/tests correction after independent verification failed on old-server normalization and existing nonblank satellite renewal.
- `GET /health` now advertises `capabilities.isolated_session_registration: true`. Pi requires the literal boolean before each satellite registration flight, including renewal, then sends `isolated: true`. Missing, false, and malformed capability values fail closed with upgrade guidance; no version floor or automatic project inference was added.
- `Store.RegisterIsolatedSession` uses the existing registration transaction to validate the requested root and selected continuation before ownership repair, sync mutations, or lease renewal. Any existing nonblank directory returns `409 session_isolation_conflict`; directories are never silently cleared. New/blank satellites retain empty directory. Ordinary runtime registrations keep their original normalization and ownership rules.
- RED: `cd plugin/pi && npm test` failed 7 new assertions (252/259 passed): absent isolated wire flag and missing/false/malformed capability allowed registration. `go test ./internal/server/... ./internal/store/...` failed the new health capability, runtime-bound root/continuation, ended root, and invalid isolated request regressions, in addition to the two known socket environment failures.
- GREEN: final `cd plugin/pi && npm test` passed 260/260. Final Go package run passed store and all isolation/directory regressions; server still fails only `TestUnixSocketServesHTTPWithRestrictivePermissions` and `TestUnixSocketCloseIsIdempotent`, both with `engram server: socket parent hierarchy is writable by an untrusted user` (known on HEAD 4cb56ac). `go vet ./internal/server/... ./internal/store/...` and `git diff --check` passed. Changed Go files were normalized with targeted gofmt.
- TRIANGULATE: coverage includes capability recheck on renewal, same-project success without capability, rejected registration preserving session/lease/sync journal, ownerless legacy roots not repaired, new and existing empty satellites, server-selected continuations, whitespace directory inputs, and runtime candidate exclusion.
- Intermediate validation found two satellite fixtures missing the newly required capability (fixed without weakening assertions), and one transient failure in `a later ownership conflict on an uncertain replacement revokes shutdown end across reload`; that unchanged test passed on both subsequent full Pi runs. Root cause of that transient result is not established.
- No staging or commits performed. Existing unrelated dirty/untracked work preserved. Compatibility implementation is ready for parent review, but overall task closure remains pending.

## Independent-gate correction (closure pending)

- P1: Satellite registrations now supply a transport-only pre-dispatch capability guard. It runs before every POST attempt, including refusal recovery/reconnect, outside transport-error classification. A guarded request permits only one recovery replay; ordinary runtime registration and other POST semantics remain unchanged. Corrected core still enforces `isolated: true` atomically.
- P2: Satellite acknowledgements require the exact root or `:resume:` followed only by one or more ASCII decimal digits. This matches core's arbitrary-precision allocator, including existing zero/leading-zero numeric identities. Empty, nonnumeric, negative, and trailing suffixes cannot authorize observation writes or shutdown cleanup.
- Lease evidence: Server rejection compares full session structs rather than JSON (which omits `RuntimeLeaseExpiresAt`). Direct store regression snapshots every sessions and sync_mutations field before/after a rejected runtime-bound continuation, including ownerless continuation; lease, ownership, and journal stay unchanged.
- Real-server evidence: Extended the existing native persistence harness (its Go bridge builds successfully inside the test sandbox, no skip) to persist a B observation under the B-owned satellite, assert empty satellite directory, and compare principal runtime A before/after unchanged. Core handler tests directly assert `ActiveRuntimeSessions` excludes isolated satellites; the Pi bridge has no runtime-candidate HTTP endpoint, so integration proves the empty-directory premise rather than invoking that store method through a new test framework.
- RED: `cd plugin/pi && npm test` observed seven intended new failures (260/267 passed): five malformed acknowledgements wrote successfully and both changing-server transport fixtures bypassed capability validation. Recovery fixture was then narrowed to the actual two-attempt registration policy so the replacement is reached immediately after recovery.
- GREEN: Final `cd plugin/pi && npm test` passed 272/272, including real-server build/persistence, changing-server retry/recovery refusals, capable-server recovery, bounded persistent refusal, numeric acknowledgement edge cases, and malformed-ack cleanup denial.
- Added Go evidence regressions pass without core changes (test-only evidence expansion, not an invented RED). `go test ./internal/server/... ./internal/store/...` passes store and isolation regressions, but still fails only the two known Unix-socket hierarchy tests. Socket sources/tests remain untouched; T3 is not started.
- Intermediate checks: one Pi run timed out after an unconditional optional-callback await changed unguarded dispatch scheduling; guard now awaits only when supplied, preserving ordinary runtime scheduling. The next run caught an extracted-source test helper still stripping the old recursive TypeScript call; helper updated to the new signature, then full suite passed.
- Final normalization: `gofmt -w internal/store/isolated_session_test.go internal/server/isolated_session_test.go`. `go vet ./internal/server/... ./internal/store/...` and `git diff --check` passed.
- T1/T2 remain unchecked for parent closure. Dirty work preserved; no staging/commits.

## Next step

User requested T3 as the final task, after T1/T2. Its applicable checks include both socket tests and the complete server package; use observed RED/GREEN and verify the directory trust protection remains intact.

Parent: review the compatibility correction and the combined dirty diff, independently verify, create the scoped work-unit commits, and record hashes here before closing.
