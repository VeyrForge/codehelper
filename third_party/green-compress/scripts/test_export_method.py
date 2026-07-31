#!/usr/bin/env python3
"""Light CPU checks: IQ ENGINE_PASS_THROUGH keep (no Q4 crush)."""
from __future__ import annotations

import os
import sys

import numpy as np
from gguf.constants import GGMLQuantizationType

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from gguf_util import (  # noqa: E402
    ENGINE_PASS_THROUGH,
    EXL3_TENSOR_PREFIX,
    EXL3_TYPE_ID,
    choose_export_payload,
    default_requant_for_reader,
)


def test_engine_pass_through_includes_iq() -> None:
    for name in (
        "IQ2_XXS",
        "IQ2_XS",
        "IQ2_S",
        "IQ3_XXS",
        "IQ3_S",
        "IQ1_S",
        "IQ1_M",
        "IQ4_NL",
        "IQ4_XS",
    ):
        assert getattr(GGMLQuantizationType, name) in ENGINE_PASS_THROUGH, name


def test_iq2_xxs_passthrough_not_crushed_to_q4() -> None:
    """requant=none must keep IQ bytes (without pass-through -> Q4_0)."""

    class _Fake:
        name = "blk.0.attn_q.weight"
        shape = (64, 64)
        tensor_type = GGMLQuantizationType.IQ2_XXS
        data = np.arange(256, dtype=np.uint8)

    data, qtype, label = choose_export_payload(_Fake(), "green_optimal", requant="none")
    assert qtype is GGMLQuantizationType.IQ2_XXS
    assert label == "passthrough_iq2_xxs"
    assert np.array_equal(data, _Fake.data)


def test_default_requant_none_for_iq_mix() -> None:
    class _T:
        def __init__(self, name: str, qtype: GGMLQuantizationType) -> None:
            self.name = name
            self.shape = (64, 64)
            self.tensor_type = qtype

    class _Reader:
        tensors = [
            _T("blk.0.attn_q.weight", GGMLQuantizationType.IQ2_XXS),
            _T("blk.0.attn_v.weight", GGMLQuantizationType.IQ3_S),
        ]

    assert default_requant_for_reader(_Reader()) == "none"


def test_exl3_choose_export_payload_passes_through() -> None:
    """EXL3-prefixed tensors must be returned verbatim with label==EXL3_TYPE_ID."""

    class _FakeExl3:
        name = "exl3:blk.0.attn_q.weight"
        shape = (32, 64)
        tensor_type = None  # EXL3 guard fires before type check
        data = np.arange(128, dtype=np.uint8)

    # tensor_type is accessed only after the EXL3 guard in choose_export_payload;
    # set a sentinel so we know it is never reached.
    _FakeExl3.tensor_type = type('_Sentinel', (), {})()  # type: ignore[assignment]

    data, _qtype, label = choose_export_payload(_FakeExl3(), "green_optimal", requant="none")
    assert label == EXL3_TYPE_ID, f"expected {EXL3_TYPE_ID!r}, got {label!r}"
    assert np.array_equal(data, _FakeExl3.data), "payload bytes must be unchanged"


def main_light() -> None:
    test_engine_pass_through_includes_iq()
    test_iq2_xxs_passthrough_not_crushed_to_q4()
    test_default_requant_none_for_iq_mix()
    test_exl3_choose_export_payload_passes_through()
    print("ok: 4 light export/pass-through tests passed")


if __name__ == "__main__":
    main_light()