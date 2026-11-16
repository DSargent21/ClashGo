# Progress

Single source of truth for "where did the last session stop". A fresh session
reads this first, then `BUILD_PLAN.md`. Never rely on chat history.

## Now

- Objective: reliable unattended farming + deeper offline tests.
- Next task: **F.1 Dead-base / collector detection** (`BUILD_PLAN.md`).
- Last green commit: see `git log -1`.
- Verify gate: `./tools/verify.sh` (full) / `./tools/verify.sh --fast`.

## Blockers

- None.

## Session log

Append one entry per session, newest at the bottom. Keep it short and factual.

Format:

```
### YYYY-MM-DD — <task id>: <title>
- Changed: <files/packages>
- Verified: <command> -> PASS|FAIL (<note>)
- Commit: <short sha or "none">
- Next: <task id or blocker>
```

### 2026-11-16 — H.1: persistent harness + verification gate
- Changed: `AGENTS.md`, `BUILD_PLAN.md`, `PROGRESS.md`, `DECISIONS.md`,
  `tools/verify.sh`, `Makefile`, `.agents/skills/*`
- Verified: `./tools/verify.sh` -> see baseline below
- Commit: none (worktree change)
- Next: F.1 dead-base detection

### Baseline at harness setup (2026-11-16)
- `go vet ./...` -> PASS
- `go test ./...` (with OpenCV rpath) -> PASS
- `go test -race ./...` (with OpenCV rpath) -> PASS
- `go build ./...` -> PASS
