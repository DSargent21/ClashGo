"""Spell-placement contract checks for every shipped army.

Two armies ship today:
- auto_edrag_rush.yaml  (Rage + Ice spells, formula-driven lines)
- valk_spam.yaml        (Earthquake spell, one formula point; Valkyries on a
                         formula-driven "sides" ring covering all four sides)

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


def _formula_lines(entry: dict) -> list[dict]:
    """The sub-lines a spell entry fires along.

    Rage ships as `"type": "lines"` — one entry carrying its sub-lines — since
    the picker began writing the live frame (`pkg/formula/formula.go`, the
    `lines` schema). A plain `line` entry is the single-sub-line form.
    """
    if entry.get("type") == "lines" or entry.get("lines"):
        return entry["lines"]
    return [entry]


def test_edrag_rush_formula_spells_have_valid_geometry():
    f = _load_json("auto_edrag_rush_formula.json")
    for name in ("rage spell", "ice spell"):
        entry = f["units"][name]
        lines = _formula_lines(entry)
        assert lines, f"{name}: entry carries no sub-line at all (type={entry.get('type')!r})"
        for i, line in enumerate(lines):
            length = math.hypot(
                line["p2"]["x"] - line["p1"]["x"], line["p2"]["y"] - line["p1"]["y"]
            )
            assert length > 20, (
                f"{name}[{i}]: pinned line is only {length:.0f}px long — "
                f"spells would stack on one tile"
            )


def test_edrag_rush_inner_rage_line_pinned_deeper_than_outer():
    """The inner rage sub-line must sit closer to screen center than the outer
    one, or the auto-split drops the 'deep' rage on top of the entry rage."""
    f = _load_json("auto_edrag_rush_formula.json")
    screen = f["screen"]
    cx, cy = screen["w"] / 2, screen["h"] / 2

    def center_dist(line):
        mx = (line["p1"]["x"] + line["p2"]["x"]) / 2
        my = (line["p1"]["y"] + line["p2"]["y"]) / 2
        return math.hypot(cx - mx, cy - my)

    units = f["units"]
    if units.get("_rage_inner") is not None:
        # Legacy helper entry: a separate `_rage_inner` line beside the rage one.
        outer, inner = units["rage spell"], units["_rage_inner"]
    else:
        lines = _formula_lines(units["rage spell"])
        assert len(lines) >= 2, (
            "rage spell must carry an outer and an inner sub-line (the 3+2 split)"
        )
        outer, inner = lines[0], lines[1]
    assert center_dist(inner) < center_dist(outer), (
        "the inner rage sub-line must be closer to the base center than the outer one"
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


def test_valk_spam_earthquake_drops_on_a_single_point():
    """The valk army drops the WHOLE spell card on one pinned spot. FourSides
    would ring all four walls instead, which is a different attack."""
    doc = _load_yaml("valk_spam.yaml")
    phases = _spell_phases(doc)
    assert len(phases) == 1, "valk_spam must have exactly one spell phase"
    assert phases[0]["pattern"] == "Point", (
        "Earthquake phase must use Point so the spell card lands on the one spot "
        "the formula pins"
    )


def _ring_entry() -> dict:
    f = _load_json("valk_spam_formula.json")
    entry = f["units"].get("valkyrie")
    assert entry is not None, (
        "valk_spam_formula.json has no 'valkyrie' entry — the ring phase would "
        "fall back to the legacy FourSides edge spam"
    )
    return entry


def test_valk_ring_is_a_sides_entry_not_a_single_line():
    """`sides` is the ring type: the card is spread across EVERY sub-line in the
    same battle. A `line` entry sweeps one flank, and a `lines` entry is a
    spell's sub-lines — either one would silently drop the ring."""
    entry = _ring_entry()
    assert entry["type"] == "sides", (
        f"valkyrie must be a \"sides\" ring, got {entry['type']!r}"
    )
    assert len(entry["lines"]) >= 4, (
        f"ring carries {len(entry['lines'])} sides; wrapping the base needs 4"
    )


def test_valk_ring_wraps_the_base_not_one_flank():
    """The point of a ring is coverage, and a base is wider than it is tall *in
    sides*: every drawn side must sit in a different quadrant of the frame.

    Quadrants rather than named edges (top/right/bottom/left) because the tool
    draws free-hand: a diamond ring — the shape of a diamond-shaped base — has
    two lines on the left and two on the right and covers no horizontal edge at
    all, while still wrapping the base completely. An edge-name check would call
    that shape broken.
    """
    f = _load_json("valk_spam_formula.json")
    w, h = f["screen"]["w"], f["screen"]["h"]
    quadrants = set()
    for line in _ring_entry()["lines"]:
        mid_x = (line["p1"]["x"] + line["p2"]["x"]) / 2
        mid_y = (line["p1"]["y"] + line["p2"]["y"]) / 2
        quadrants.add(("left" if mid_x < w / 2 else "right", "upper" if mid_y < h / 2 else "lower"))
    want = {(x, y) for x in ("left", "right") for y in ("upper", "lower")}
    assert quadrants == want, (
        f"ring covers {sorted(quadrants)} but not {sorted(want - quadrants)} — "
        "the Valkyries would bunch on part of the base instead of wrapping it"
    )


def test_valk_ring_sides_are_tappable_and_not_degenerate():
    """Every ring point must be inside the frame and inside the band the formula
    was drawn against.

    The band matters because the deploy path clamps to it: a point outside it is
    not where the user drew it, and the attack lands somewhere they never chose.
    That contract lives in the file, so it is checked here rather than left to
    the live log to notice.
    """
    f = _load_json("valk_spam_formula.json")
    w, h = f["screen"]["w"], f["screen"]["h"]
    band = f.get("tap_band")
    assert band, (
        "valk_spam_formula.json declares no tap_band, so the bot clamps to its "
        "built-in range and moves any point the user drew outside it"
    )
    for i, line in enumerate(_ring_entry()["lines"]):
        length = math.hypot(
            line["p2"]["x"] - line["p1"]["x"], line["p2"]["y"] - line["p1"]["y"]
        )
        assert length > 100, f"ring side {i} is only {length:.0f}px long"
        for pt in (line["p1"], line["p2"]):
            assert 0 <= pt["x"] < w and 0 <= pt["y"] < h, (
                f"ring side {i}: point {pt} outside the {w}x{h} frame"
            )
            assert band["y_top"] <= pt["y"] <= band["y_bottom"], (
                f"ring side {i}: point {pt} is outside the declarated tap_band "
                f"{band['y_top']}..{band['y_bottom']}; the bot would clamp it"
            )


def test_valk_other_points_are_inside_the_tap_band_too():
    """The same clamp applies to every point unit, not just the ring: a hero or
    spell spot outside the band is moved before it is tapped."""
    f = _load_json("valk_spam_formula.json")
    band = f["tap_band"]
    for name, entry in f["units"].items():
        if entry.get("type") != "point":
            continue
        pt = entry["p"]
        assert band["y_top"] <= pt["y"] <= band["y_bottom"], (
            f"{name} at {pt} is outside the tap_band {band['y_top']}..{band['y_bottom']}"
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


def test_edrag_formula_declares_the_frame_its_points_are_authored_in():
    """`screen` is the SOURCE geometry the deploy path projects from
    (`Formula.ProjectUniform`), and the picker writes the live frame's own
    pixels plus the frame it captured them on. A declaration that does not
    describe those pixels — a reference-sized 860x732 header over live 1280x720
    points — silently shifts every tap by tens of pixels, so the header must be
    a real landscape frame and every authored point must fit inside it."""
    f = _load_json("auto_edrag_rush_formula.json")
    w, h = f["screen"]["w"], f["screen"]["h"]
    assert w > h, (
        f"formula screen {w}x{h} is not a landscape live frame; the reference-"
        "era 860x732 header must not be used over live-authored points"
    )

    def _points(entry):
        if entry.get("p"):
            yield entry["p"], entry["p"]
            return
        for line in _formula_lines(entry):
            yield line["p1"], line["p2"]

    checked = 0
    for name, entry in f["units"].items():
        for p1, p2 in _points(entry):
            for pt in (p1, p2):
                assert 0 <= pt["x"] < w and 0 <= pt["y"] < h, (
                    f"{name}: point {pt} lies outside the {w}x{h} frame the "
                    "formula declares as its source geometry"
                )
                checked += 1
    assert checked > 0, "formula declares no points at all"


def test_no_spell_unit_lacks_a_formula_entry():
    """Every spell unit across all armies must be reachable by a formula entry.
    Without one the spell falls through to the legacy pCfg grid, which knows
    nothing about the side this attack was drawn for."""
    for yaml_name, json_name in (
        ("auto_edrag_rush.yaml", "auto_edrag_rush_formula.json"),
        ("valk_spam.yaml", "valk_spam_formula.json"),
    ):
        formula = _load_json(json_name)
        for u in _spell_units(_load_yaml(yaml_name)):
            assert u["name"].lower() in formula["units"], (
                f"{yaml_name}: {u['name']} has no entry in {json_name} — it would "
                "deploy on the legacy grid instead of the drawn geometry"
            )


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
