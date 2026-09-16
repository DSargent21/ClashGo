"""Strategy YAML contract checks.

Verifies the shipped strategy files parse and honor their declared knobs:
- every shipped strategy parses as valid YAML with the expected keys
- valk_spam.yaml auto-ends the battle at 50% destruction (end_at_percent: 50)
- other strategies leave end_at_percent unset (0) so they run to the stall timer
"""

from pathlib import Path

import yaml

STRATEGY_DIR = Path(__file__).resolve().parents[1] / "assets" / "strategies"

STRATEGIES = sorted(p.name for p in STRATEGY_DIR.glob("*.yaml"))


def _load(name: str) -> dict:
    data = (STRATEGY_DIR / name).read_text()
    parsed = yaml.safe_load(data)
    assert isinstance(parsed, dict), f"{name} did not parse to a mapping"
    return parsed


def test_all_shipped_strategies_parse():
    assert STRATEGIES, "no strategy YAML files found"
    for name in STRATEGIES:
        doc = _load(name)
        assert doc.get("name"), f"{name} missing name"
        assert doc.get("target_edge"), f"{name} missing target_edge"
        assert isinstance(doc.get("phases"), list) and doc["phases"], (
            f"{name} has no phases"
        )


def test_valk_spam_ends_battle_at_50_percent():
    doc = _load("valk_spam.yaml")
    assert doc["end_at_percent"] == 50, (
        "valk_spam.yaml must auto-end the battle at 50% destruction"
    )
    # The strategy must also actually deploy Valkyries.
    unit_names = {
        unit["name"]
        for phase in doc["phases"]
        for unit in phase.get("units", [])
    }
    assert "Valkyrie" in unit_names


def test_valk_spam_arms_army_slot_4():
    doc = _load("valk_spam.yaml")
    assert doc["army_slot"] == 4, (
        "valk_spam.yaml must arm the 4th saved recipe (the Valkyrie loadout)"
    )


def test_other_strategies_do_not_force_early_end():
    for name in STRATEGIES:
        if name == "valk_spam.yaml":
            continue
        doc = _load(name)
        assert not doc.get("end_at_percent"), (
            f"{name} unexpectedly declares end_at_percent; "
            "only valk_spam should auto-end early"
        )


def test_other_strategies_default_to_army_slot_1():
    for name in STRATEGIES:
        if name == "valk_spam.yaml":
            continue
        doc = _load(name)
        assert not doc.get("army_slot"), (
            f"{name} unexpectedly declares army_slot; "
            "only valk_spam targets a non-default saved recipe"
        )
