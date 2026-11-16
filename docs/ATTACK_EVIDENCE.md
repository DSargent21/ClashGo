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

## Run 11 — a live refusal, and the report bug it exposed

`logs/live_hero_firsttap_run11.log` (full army, 1 star) is the counterexample
that must stay in this record. The pinned point (321,207) was refused for the
siege three times (`siege_placements=0`, `siege_retries=3`) and then refused for
both heroes; each hero landed on its first retry on the troop line. The battle
ran without the machine, yet the verifier logged `all units successfully
deployed` and `last_attack_report.json` said `deploy_success: true`,
`undeployed_slots: 0`. That false success is fixed by counting `Siege` in
`checkRemainingSlots` and refusing further field taps from `retryDeploy`; the
CLI now prints `Stone Slammer FAILED` and exits 1 for that shape of log
(`tmp/siege_fail.log`). Run 11's own report was written before the fix and still
shows the old result.

The siege ground list now leads with troop-line points near the refused tile and
clamps every candidate to the deployable band; run 11's outward walk had put one
retry at y=94, behind the top HUD. The hero phase also refuses to lead with a
ground the siege just proved refused. These are gate/test-proven only: run 11
spent the army, so no battle has exercised them yet.

## Run 12 — the line leads, the first selection still refused

`logs/live_hero_firsttap_run12.log` (full army, 1 star) tested the line-first
hero ground. Both heroes logged `ground_source=troop_line` and first tap
(293,507); both first taps were refused, and both retries on line ground
(266,487) landed. So the authored-ground theory was incomplete: the FIRST
selection of a hero card did not arm at pin (run 11), formula (run 10) or line
ground (run 12), while the NEXT selection landed in all three. `PrimeSlot` now
spends one extra card SELECTION tap before the first field tap (no placement,
no re-arm, no ability budget); it is covered by `TestHeroFirstTap_*`, not by a
battle, because run 12 spent the army.

Siege in run 12 was refused on all three line-near retries
(`siege_placements=0`, `siege_retries=3`). The verifier counted it
(`last_attack_report.json`: `deploy_success: false`, `undeployed_slots: 1`) but
logged a blank `unit=` for the unlinked fallback slot; `cmd/attack_report`
therefore still printed a clean verdict. `markSiegeFailed` now names the slot.

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
