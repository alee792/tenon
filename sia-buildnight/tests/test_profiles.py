"""Validate our SIA profile JSONs against SIA's required-key contract, so a typo
can't fail a run on the night. Mirrors sia/profiles.py `_require(...)`.

Credential-free: only reads/parses local JSON, no model or network.
"""

import json
from pathlib import Path

PROFILES = Path(__file__).resolve().parents[1] / "kit" / "profiles"

META_KEYS = {"profile_id", "name", "agent_impl", "model", "provider_id"}
TARGET_KEYS = {"profile_id", "name", "model", "provider_id"}
KNOWN_IMPLS = {"claude", "openhands", "pydantic-ai"}


def _load(name: str) -> dict:
    return json.loads((PROFILES / name).read_text(encoding="utf-8"))


def test_all_profiles_are_valid_json():
    for p in PROFILES.glob("*.json"):
        json.loads(p.read_text(encoding="utf-8"))  # raises on malformed JSON


def test_meta_profiles_have_required_keys_and_known_impl():
    metas = list(PROFILES.glob("meta-*.json"))
    assert metas, "expected at least one meta profile"
    for p in metas:
        data = _load(p.name)
        assert META_KEYS <= data.keys(), f"{p.name} missing {META_KEYS - data.keys()}"
        assert data["profile_id"] == p.stem, f"{p.name} profile_id must equal filename stem"
        assert data["agent_impl"] in KNOWN_IMPLS, f"{p.name} unknown agent_impl {data['agent_impl']!r}"


def test_openhands_variant_exists_for_the_file_based_path():
    data = _load("meta-buildnight-openhands.json")
    assert data["agent_impl"] == "openhands"  # unlocks GUIDANCE.md / ledger.jsonl carrier


def test_target_profile_has_required_keys_and_agent_reference():
    data = _load("target-buildnight.json")
    assert TARGET_KEYS <= data.keys(), f"missing {TARGET_KEYS - data.keys()}"
    assert "agent_reference" in data
    assert data["agent_reference"]["entrypoint"] == "target_agent.py"
