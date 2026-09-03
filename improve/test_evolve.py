#!/usr/bin/env python3
"""Tests for improve/evolve.py's spec validation.

The gene layout is the one piece of spec config that decides what the search
recombines, and a wrong value there does not fail — it quietly searches the
wrong space. So the shapes it refuses are worth a test even where the
surrounding module has none.

Stdlib self-runner, no pytest: `python3 improve/test_evolve.py`.
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import evolve  # noqa: E402


def refused(raw) -> str:
    """The one-line message `GeneLayout.of` rejects `raw` with."""
    try:
        evolve.GeneLayout.of(raw)
    except evolve.EvolveError as err:
        return str(err)
    raise AssertionError(f"genes {raw!r} should have been refused")


def test_the_default_layout_is_used_when_genes_are_omitted():
    layout = evolve.GeneLayout.of(None)
    assert layout.dirs == evolve.DEFAULT_GENE_DIRS
    assert layout.files == evolve.DEFAULT_GENE_FILES
    assert evolve.GeneLayout.of({}).dirs == evolve.DEFAULT_GENE_DIRS


def test_a_named_layout_replaces_the_default_for_that_key_only():
    layout = evolve.GeneLayout.of({"dirs": ["skills"]})
    assert layout.dirs == ("skills",)
    assert layout.files == evolve.DEFAULT_GENE_FILES


def test_a_bare_string_is_refused_rather_than_iterated_into_characters():
    """`"skills"` is the shape that must never pass: iterated, it becomes six
    loci that do not exist, and the search silently stops recombining the one
    that does instead of saying anything."""
    message = refused({"dirs": "skills"})
    assert "genes.dirs" in message, "the message must name the key that is wrong"
    assert "\n" not in message, "spec errors print as one line"
    assert "genes.files" in refused({"files": "instructions.md"})


def test_a_non_string_or_empty_entry_is_refused():
    assert "genes.dirs" in refused({"dirs": ["skills", 7]})
    assert "genes.files" in refused({"files": ["instructions.md", ""]})
    assert "genes.dirs" in refused({"dirs": ["skills", "  "]})
    assert "genes.dirs" in refused({"dirs": {"skills": True}})


def test_genes_itself_must_be_an_object():
    assert "genes" in refused(["skills"])


if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for test in tests:
        test()
        print(f"ok  {test.__name__}")
    print(f"\n{len(tests)} passed")
