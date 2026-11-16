# Army attack — evidence boundary

Verified tree: `67bf0aa` (`development`). Gate: `./tools/verify.sh` →
`VERIFY PASSED (full)`; `go test ./internal/attack ./internal/attacklog` → PASS.
All claims below were re-derived from this commit; the only working-tree changes
are the pre-existing unrelated `CHANGELOG.md`, `Makefile` and untracked
`tools/*.sh`.

Live artifacts are local (logs are gitignored). Paths are relative to the repo
root unless absolute.

## Battle-proven claims

| Claim | Evidence |
|---|---|
| Siege lands on the first tap when the pin ground is accepted, and retries are accounted separately | `logs/live_polish_run10.log` lines 115–116 (`siege placed on the user's pinned hero point`, `reconcile: siege count label gone; slot drained`) and line 157 (`siege_placements=1 ... siege_retries=0`). Same in run 9 line 142. |
| Siege retry path lands after one refused ground and is not counted as a second machine | `logs/live_polish_run6.log`: first tap refused, retry at `{"X":877,"Y":441}` logs `reconcile: siege count label gone`, summary `siege_placements=1 siege_retries=1`. |
| Every detected troop leaves its card | Run 10: verifier lines 177–178 confirm both troop cards empty (`verifier: slot visibly empty; deployment confirmed` for Electro Dragon and Balloon), `sweep complete ... deployed=0` (line 173), all units confirmed (line 179). Run 9: sweep found zero troop cards and the battle ended 3 stars; see the run-9 report note below. |
| Both heroes deploy with at most one re-arm and no repeated tapping | `logs/live_polish_run10.log` lines 143–155: each hero has exactly one `re-arming ... rearms=1`, one `hero deployed (single card tap + field-tap retry on new ground)` with `field_retry=1`, then `hero ability activated`. No second re-arm or extra field tap for either hero. |
| Attack-log verdict matches the observed deployment | `logs/live_polish_run10_report.txt` (produced by `go run ./cmd/attack_report -log logs/live_polish_run10.log`): `deployed=7`, `not in bar=4`, `UNVERIFIED=0`, verdict “nothing in the log contradicts a successful deployment”. `logs/live_polish_run10_last_attack_report.json`: `deploy_success: true`, `undeployed_slots: 0`. |
| Report under-claim from run 9 is closed | Run 9's report kept Balloon/Electro Dragon/Royal Champion UNVERIFIED because the verifier closed empty cards without a log or state change. That path now logs the positive confirmation above; run 10 reports all seven bar units deployed. `logs/live_polish_run9.log` plus `last_attack_report.json` (timestamp `2026-11-15T21:52:54`) are the before case. |

## Coverage-only claims (no live battle yet)

| Claim | Coverage |
|---|---|
| A genuinely unplaced unit yields FAILED and flags the run | `TestDeployTroops_RefusedGroundNeverMarksDeployed`, `TestParseVerifierRemainingFlagsRunFailed`, `TestCheckRemainingSlots_CountsVisiblyNonEmptyFailedSlot` (`internal/attack`, `internal/attacklog`). No live battle in this session ended with a unit on the bar. |
| Positive confirmation promotes a slot to deployed | `TestCheckRemainingSlots_EmptyCardConfirmsDeployment`, `TestParseVerifierEmptyCardConfirmsHandoff`; also observed live in run 10 for both troop cards. |
| A drained-but-unobserved unit stays unknown and names its missing evidence | `TestParseVerifierUnobservedStaysUnknownWithEvidence`, `TestLogUnaccountedSlots_NamesUnobservedUnit`. No live case. |
| Troop placement is composition-independent (small, single-type full, full mixed, partial badge) | `TestDeployTroops_CompositionIndependentCounts` — 4-unit card, 12-unit single-type, 10-unit full mixed, 10-unit other type, and a partial live badge of 3 of 10. Live runs used one full composition only. |
| Siege ground list leads outward and the truncation bug cannot recur | `TestSiegeRefusedGrounds_LeadsOutward`; `TestMarkSiegeFailed_SlotIsFailedNotDeployed`. Live runs 9–10 exercised the first-tap path, not the refused-and-retry path. |

## Known open behaviour

- **Hero first tap still needs its one bounded rescue on most bases.** Run 10
  shows both heroes refused the first field tap and recovered with exactly one
  re-arm plus one retry (lines 143–149). The rescue is bounded and preserves
  the ability in that run, but the first tap itself is not yet reliable.
- **No live FAILED battle.** The FAILED verdict and the run-failed flag are
  proven by the tests above, not by a battle this session.
- Run 9's 3-star battle is the strongest live proof that no troop card was left
  (`sweep deployed=0` plus `undeployed_slots: 0`), but its per-unit report was
  conservative (UNVERIFIED) before the positive-confirmation change.
