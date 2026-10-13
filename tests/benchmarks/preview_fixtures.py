#!/usr/bin/env python3
"""Create synthetic Markdown and Word files for local preview measurements."""

from __future__ import annotations

import argparse
import json
import os
import zipfile
from pathlib import Path


def write_fixtures(destination: Path) -> list[dict[str, str | int | None]]:
    """Write only synthetic data, with no model calls or external assets."""
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    for limit in (24_000, 100_000):
        unit = (
            "## Local plan\n\nReview **the current draft** and keep the original intent.\n\n"
            "- Read the supplied files.\n- Save a checked revision.\n\n"
        )
        body = (unit * (limit // len(unit) + 1))[:limit]
        (destination / f"notes-{limit // 1000}k.md").write_text(body, encoding="utf-8")
    rows = ["| Item | Step | Owner | Status | Note |", "| --- | --- | --- | --- | --- |"]
    rows.extend(f"| {index} | read | local | done | saved |" for index in range(900))
    (destination / "dense-table.md").write_text("\n".join(rows), encoding="utf-8")

    def paragraph(index: int) -> str:
        return (
            "<w:p>"
            + "".join(
                f"<w:r><w:t>Paragraph {index} sample {run}: preserve local draft text "
                "and review changes. </w:t></w:r>"
                for run in range(4)
            )
            + "</w:p>"
        )

    def table(index: int, count: int, columns: int = 8) -> str:
        return (
            "<w:tbl>"
            + "".join(
                "<w:tr>"
                + "".join(
                    f"<w:tc><w:p><w:r><w:t>Table {index} row {row} cell {column}"
                    "</w:t></w:r></w:p></w:tc>"
                    for column in range(columns)
                )
                + "</w:tr>"
                for row in range(count)
            )
            + "</w:tbl>"
        )

    def word(name: str, body: str) -> int:
        xml = (
            '<?xml version="1.0" encoding="UTF-8"?><w:document '
            'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
            "<w:body>" + body + "</w:body></w:document>"
        )
        with zipfile.ZipFile(destination / name, "w", zipfile.ZIP_DEFLATED) as archive:
            members = {
                "[Content_Types].xml": (
                    '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
                    '<Override PartName="/word/document.xml" ContentType="application/vnd.'
                    'openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>'
                ),
                "word/document.xml": xml,
            }
            for member, content in members.items():
                # Fixed metadata makes the generated fixture bytes reproducible.
                info = zipfile.ZipInfo(member, date_time=(2026, 1, 1, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                archive.writestr(info, content)
        return len(xml.encode("utf-8"))

    expanded = {
        "document-medium.docx": word(
            "document-medium.docx", "".join(paragraph(i) for i in range(600)) + table(1, 100)
        ),
        "document-large.docx": word(
            "document-large.docx", "".join(paragraph(i) for i in range(4000)) + table(1, 300)
        ),
        "dense-tables.docx": word("dense-tables.docx", "".join(table(i, 12) for i in range(70))),
    }
    metadata: list[dict[str, str | int | None]] = []
    for name in sorted(["notes-24k.md", "notes-100k.md", "dense-table.md", *expanded]):
        path = destination / name
        os.chmod(path, 0o600)
        metadata.append(
            {"name": name, "bytes": path.stat().st_size, "xml_bytes": expanded.get(name)}
        )
    return metadata


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="Directory for synthetic files")
    args = parser.parse_args()
    print(json.dumps(write_fixtures(args.output), indent=2))


if __name__ == "__main__":
    main()
