"""Spell-placement contract checks for every shipped army.

Two armies ship today:
- auto_edrag_rush.yaml  (Rage + Ice spells, formula-driven lines)
- valk_spam.yaml        (Earthquake spell, FourSides ring)

These tests pin the YAML <-> formula.json contract so a spell placement
regression (renamed unit, missing formula entry, wrong pattern, invalid
geometry) fails here before it fails in a live attack.
"""

import json
import math
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
STRATEGY_DIR = ROOT / "assets" / "strategies"

SPELL_NAMES = {
    "Rage Spell",
    "Ice Spell",
    "Earthquake Spell",
    "Poison Spell",
    "Heal Spell",
    "Jump Spell",
    "Lightning Spell",
    "Freeze Spell",
    "Clone Spell",
    "Invisibility Spell",
    "Recall Spell",
    "Skeleton Spell",
    "Bat Spell",
    "Haste Spell",
}

CORNER_OVERRIDES = {"TopLeft", "TopRight", "BottomRight", "BottomLeft"}


def _load_yaml(name: str) -> dict:
    doc = yaml.safe_load((STRATEGY_DIR / name).read_text())
    assert isinstance(doc, dict), f"{name} did not parse to a mapping"
    return doc


def _load_json(name: str) -> dict:
    return json.loads((STRATEGY_DIR / name).read_text())


def _spell_phases(doc: dict) -> list[dict]:
    return [
        phase
        for phase in doc["phases"]
        if any(u["name"] in SPELL_NAMES for u in phase.get("units", []))
    ]


def _spell_units(doc: dict) -> list[dict]:
    return [u for phase in doc["phases"] for u in phase.get("units", []) if u["name"] in SPELL_NAMES]


# ---------------------------------------------------------------------------
# Army 1: auto_edrag_rush — Rage + Ice, formula-driven
# ---------------------------------------------------------------------------


def test_edrag_rush_declares_both_spell_units():
    doc = _load_yaml("auto_edrag_rush.yaml")
    spells = {u["name"] for u in _spell_units(doc)}
    assert spells == {"Rage Spell", "Ice Spell"}, (
        f"auto_edrag_rush spells drifted: {sorted(spells)}"
    )


def test_edrag_rush_spell_phase_uses_line_pattern():
    doc = _load_yaml("auto_edrag_rush.yaml")
    phases = _spell_phases(doc)
    assert len(phases) == 1, "auto_edrag_rush must have exactly one spell phase"
    assert phases[0]["pattern"] == "Line", (
        "spell phase pattern must be Line so formula coordinates drive it"
    )


def test_edrag_rush_formula_covers_both_spells():
    f = _load_json("auto_edrag_rush_formula.json")
    units = f["units"]
    for name in ("rage spell", "ice spell"):
        entry = units.get(name)
        assert entry is not None, f"formula is missing '{name}' — the spell would fall back to unconfigured legacy edges"


def test_edrag_rush_formula_spells_have_valid_geometry():
    f = _load_json("auto_edrag_rush_formula.json")
    for name in ("rage spell", "ice spell"):
        entry = f["units"][name]
        assert entry["type"] == "line", f"{name}: expected a pinned line, got {entry['type']}"
        p1, p2 = entry["p1"], entry["p2"]
        length = math.hypot(p2["x"] - p1["x"], p2["y"] - p1["y"])
        assert length > 20, (
            f"{name}: pinned line is only {length:.0f}px long — spells would stack on one tile"
        )


def test_edrag_rush_inner_rage_line_pinned_deeper_than_outer():
    """The _rage_inner line must sit closer to screen center than the rage
    outer line, or the auto-split drops the 'deep' rage on top of the entry rage."""
    f = _load_json("auto_edrag_rush_formula.json")
    screen = f["screen"]
    cx, cy = screen["w"] / 2, screen["h"] / 2

    def center_dist(line):
        mx = (line["p1"]["x"] + line["p2"]["x"]) / 2
        my = (line["p1"]["y"] + line["p2"]["y"]) / 2
        return math.hypot(cx - mx, cy - my)

    rage = f["units"]["rage spell"]
    inner = f["units"]["_rage_inner"]
    assert center_dist(inner) < center_dist(rage), (
        "_rage_inner must be closer to the base center than the rage entry line"
    )


def test_edrag_rush_corner_overrides_cover_spells():
    f = _load_json("auto_edrag_rush_formula.json")
    overrides = f.get("corner_overrides", {})
    for corner in overrides:
        assert corner in CORNER_OVERRIDES, f"unknown corner key {corner!r}"
        for name in ("rage spell", "ice spell"):
            assert name in overrides[corner], (
                f"corner_overrides[{corner}] is missing '{name}' — "
                "that corner would silently mix mirrored BR geometry with overrides"
            )


def test_edrag_rush_override_spell_geometry_in_bounds_and_valid():
    f = _load_json("auto_edrag_rush_formula.json")
    w, h = f["screen"]["w"], f["screen"]["h"]
    for corner, units in f.get("corner_overrides", {}).items():
        for name in ("rage spell", "ice spell"):
            entry = units[name]
            p1, p2 = entry["p1"], entry["p2"]
            for pt in (p1, p2):
                assert 0 <= pt["x"] < w and 0 <= pt["y"] < h, (
                    f"{corner}/{name}: point {pt} outside the {w}x{h} screen"
                )
            length = math.hypot(p2["x"] - p1["x"], p2["y"] - p1["y"])
            assert length > 20, (
                f"{corner}/{name}: line collapsed to {length:.0f}px"
            )


# ---------------------------------------------------------------------------
# Army 2: valk_spam — Earthquake, FourSides ring
# ---------------------------------------------------------------------------


def test_valk_spam_declares_earthquake_spell():
    doc = _load_yaml("valk_spam.yaml")
    spells = [u for u in _spell_units(doc)]
    assert {u["name"] for u in spells} == {"Earthquake Spell"}, (
        f"valk_spam spells drifted: {sorted(u['name'] for u in spells)}"
    )


def test_valk_spam_earthquake_uses_foursides_with_deep_offset():
    doc = _load_yaml("valk_spam.yaml")
    phases = _spell_phases(doc)
    assert len(phases) == 1, "valk_spam must have exactly one spell phase"
    phase = phases[0]
    assert phase["pattern"] == "FourSides", (
        "Earthquake phase must use FourSides so EQs ring all four walls"
    )
    assert phase.get("offset", 0) > 0, (
        "Earthquake phase needs a positive offset — without it EQs land on the "
        "outer red line instead of deeper in on the walls"
    )


def test_valk_spam_earthquake_runs_after_troops():
    doc = _load_yaml("valk_spam.yaml")
    names = [p["name"] for p in doc["phases"]]
    spell_idx = next(
        i for i, p in enumerate(doc["phases"]) if any(u["name"] in SPELL_NAMES for u in p["units"])
    )
    assert spell_idx > 0, "spell phase must not run first"
    assert "Valkyrie Spam" in names[:spell_idx], (
        "Valkyries must deploy before the Earthquake ring"
    )


def test_valk_spam_spells_carry_amount_all():
    doc = _load_yaml("valk_spam.yaml")
    for u in _spell_units(doc):
        assert u.get("amount") == "All", (
            f"{u['name']}: amount must be 'All' so the live OCR count drives taps"
        )


def test_edrag_rush_spells_carry_amount_all():
    doc = _load_yaml("auto_edrag_rush.yaml")
    for u in _spell_units(doc):
        assert u.get("amount") == "All", (
            f"{u['name']}: amount must be 'All' so the live OCR count drives taps"
        )


# ---------------------------------------------------------------------------
# Cross-army: formula reference frame consistency
# ---------------------------------------------------------------------------


def test_edrag_formula_screen_matches_reference_frame():
    f = _load_json("auto_edrag_rush_formula.json")
    assert f["screen"] == {"w": 860, "h": 732}, (
        "formula must be authored on the 860x732 reference frame the "
        "mirror/scale math expects"
    )


def test_no_spell_unit_lacks_both_formula_and_pattern_fallback():
    """Every spell unit across all armies must be reachable by at least one
    placement path: formula entry (edrag) or a configured legacy pattern
    (valk FourSides)."""
    edrag_formula = _load_json("auto_edrag_rush_formula.json")
    valk = _load_yaml("valk_spam.yaml")

    edrag = _load_yaml("auto_edrag_rush.yaml")
    for u in _spell_units(edrag):
        assert u["name"].lower() in edrag_formula["units"], (
            f"{u['name']} has neither formula entry nor legacy fallback"
        )

    # valk spells rely on the FourSides phase pattern, verified above.
    assert all(p["pattern"] == "FourSides" for p in _spell_phases(valk))


# ---------------------------------------------------------------------------
# valk auto-end semantics (end_at_percent)
# ---------------------------------------------------------------------------


def test_valk_auto_end_threshold_is_exact_50():
    """The Go wait loop ends when currentPct >= endAtPct. 50 is the CoC
    1-star boundary: the valk ring secures the win there, so a HIGHER
    threshold burns wall time and a LOWER one forfeits the star."""
    doc = _load_yaml("valk_spam.yaml")
    assert doc["end_at_percent"] == 50


def test_valk_auto_end_is_the_only_strategy_with_a_threshold():
    for name in sorted(p.name for p in STRATEGY_DIR.glob("*.yaml")):
        doc = _load_yaml(name)
        has_threshold = bool(doc.get("end_at_percent"))
        if name == "valk_spam.yaml":
            assert has_threshold, "valk_spam must keep its auto-end"
        else:
            assert not has_threshold, (
                f"{name} sets end_at_percent — only the valk rush may "
                "auto-end early; other armies need the full battle window"
            )


def test_valk_auto_end_threshold_is_a_sensible_integer():
    doc = _load_yaml("valk_spam.yaml")
    pct = doc["end_at_percent"]
    assert isinstance(pct, int), f"end_at_percent must be an int, got {type(pct).__name__}"
    assert 1 <= pct <= 100, f"end_at_percent {pct} outside 1-100"


def test_valk_auto_end_pairs_with_army_slot_override():
    """The auto-end knob only exists on the strategy that also overrides the
    saved-army recipe — if someone re-points valk_spam at recipe 1 (or drops
    the override) the early-end no longer matches the armed loadout."""
    doc = _load_yaml("valk_spam.yaml")
    assert doc.get("army_slot") == 4 and doc.get("end_at_percent") == 50, (
        "valk_spam's early-end and army_slot: 4 must be set or cleared together"
    )


def test_stall_config_percent_roi_is_calibrated_for_auto_end():
    """end_at_percent samples destruction through stall_config.json's
    percent_roi. If that ROI is missing/empty the threshold silently never
    fires and valk battles run to the stall timer."""
    stall = json.loads((STRATEGY_DIR.parent / "stall_config.json").read_text())
    roi = stall["percent_roi"]
    x1, y1, x2, y2 = roi["Min"]["X"], roi["Min"]["Y"], roi["Max"]["X"], roi["Max"]["Y"]
    assert x2 > x1 and y2 > y1, "percent_roi collapsed — destruction reads would always be 0"
    assert (x2 - x1) >= 20 and (y2 - y1) >= 10, (
        "percent_roi too small to bracket the on-screen destruction digits"
    )
    assert stall.get("ref_width") == 860 and stall.get("ref_height") == 732, (
        "percent_roi must stay authored on the 860x732 reference frame"
    )
