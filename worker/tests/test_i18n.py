"""工作进程文案：zh、en、fr 键一致，法语标签可识别。"""

from __future__ import annotations

import re

from gogosling_worker.i18n import _TEXTS, normalize_locale, tr


def _placeholders(text: str) -> list[str]:
    return sorted(re.findall(r"\{[a-zA-Z_][a-zA-Z0-9_]*\}", text))


def test_locales_share_keys_and_placeholders() -> None:
    zh = _TEXTS["zh"]
    assert set(_TEXTS["en"]) == set(zh)
    assert set(_TEXTS["fr"]) == set(zh)
    for key, source in zh.items():
        french = _TEXTS["fr"][key]
        assert french.strip()
        assert _placeholders(french) == _placeholders(source)
        assert _placeholders(_TEXTS["en"][key]) == _placeholders(source)


def test_normalize_locale_accepts_french() -> None:
    assert normalize_locale("fr-FR") == "fr"
    assert normalize_locale("FR_ca") == "fr"
    assert normalize_locale(" fr-CA ") == "fr"
    assert normalize_locale("french") == "zh"
    assert normalize_locale("english") == "zh"
    assert normalize_locale("en_US") == "en"
    assert normalize_locale("ja") == "zh"
    assert normalize_locale(None) == "zh"
    assert "read_file" in tr("run.tool_repeat", "fr", name="read_file", count=3)
    assert tr("run.tool_repeat", "fr", name="read_file", count=3) != tr(
        "run.tool_repeat", "en", name="read_file", count=3
    )
