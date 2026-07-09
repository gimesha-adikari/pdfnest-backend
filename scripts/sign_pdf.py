#!/usr/bin/env python3

from __future__ import annotations

import json
import sys
from pathlib import Path

import fitz  # PyMuPDF


def open_document(path: str, password: str | None = None) -> fitz.Document:
    doc = fitz.open(path)

    if doc.needs_pass:
        if not password:
            raise RuntimeError("PDF is password protected.")

        if doc.authenticate(password) <= 0:
            raise RuntimeError("Invalid PDF password.")

    return doc


def main() -> int:
    if len(sys.argv) < 5:
        print(
            "Usage: sign_pdf.py input.pdf signature.png output.pdf stamps_json [password]",
            file=sys.stderr,
        )
        return 1

    input_pdf = Path(sys.argv[1])
    signature_png = Path(sys.argv[2])
    output_pdf = Path(sys.argv[3])
    stamps_json = sys.argv[4]
    password = sys.argv[5] if len(sys.argv) > 5 else None

    if not input_pdf.exists():
        print("Input PDF not found.", file=sys.stderr)
        return 1

    if not signature_png.exists():
        print("Signature image not found.", file=sys.stderr)
        return 1

    try:
        stamps = json.loads(stamps_json)
    except Exception as e:
        print(f"Invalid stamps json: {e}", file=sys.stderr)
        return 1

    if not isinstance(stamps, list) or len(stamps) == 0:
        print("No signature positions supplied.", file=sys.stderr)
        return 1

    doc = open_document(str(input_pdf), password)

    try:
        for stamp in stamps:

            page_index = int(stamp["page"]) - 1

            if page_index < 0 or page_index >= len(doc):
                continue

            page = doc[page_index]

            x = float(stamp["x"])
            y = float(stamp["y"])
            w = float(stamp["width"])
            h = float(stamp["height"])

            rect = fitz.Rect(
                x,
                y,
                x + w,
                y + h,
                )

            page.insert_image(
                rect,
                filename=str(signature_png),
                overlay=True,
            )

        doc.save(
            str(output_pdf),
            garbage=4,
            deflate=True,
            clean=True,
        )

    finally:
        doc.close()

    return 0


if __name__ == "__main__":
    raise SystemExit(main())