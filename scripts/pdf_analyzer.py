from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

import fitz  # PyMuPDF


def open_document(input_path: str, password: Optional[str] = None) -> fitz.Document:
    doc = fitz.open(input_path)

    if doc.needs_pass:
        if not password:
            raise RuntimeError("PDF is password protected but no password was provided")
        if doc.authenticate(password) <= 0:
            raise RuntimeError("Invalid PDF password")

    return doc


def rect_area(bbox: Any) -> float:
    try:
        r = fitz.Rect(bbox)
        return max(0.0, r.width * r.height)
    except Exception:
        return 0.0


def analyze_page(page: fitz.Page, page_number: int) -> Dict[str, Any]:
    page_area = max(1.0, page.rect.width * page.rect.height)

    words = page.get_text("words") or []
    text_dict = page.get_text("dict") or {}
    blocks = text_dict.get("blocks", [])

    word_count = len(words)
    text_block_count = 0
    image_block_count = 0
    text_block_area = 0.0
    image_block_area = 0.0

    for block in blocks:
        if not isinstance(block, dict):
            continue

        btype = block.get("type")
        bbox = block.get("bbox")

        if bbox is None:
            continue

        area = rect_area(bbox)

        # type 0 = text, type 1 = image in PyMuPDF's dict output
        if btype == 0:
            text_block_count += 1
            text_block_area += area
        elif btype == 1:
            image_block_count += 1
            image_block_area += area

    text_area_ratio = min(1.0, text_block_area / page_area)
    image_area_ratio = min(1.0, image_block_area / page_area)

    has_selectable_text = word_count > 0

    if word_count == 0 and image_block_count == 0:
        kind = "blank"
    elif word_count == 0 and image_block_count > 0:
        kind = "scanned"
    elif word_count > 0 and image_block_count == 0:
        kind = "text"
    else:
        # Mixed means both text and images exist on the same page.
        # OCR-layer scanned pages may also land here, but they still remain selectable.
        if image_area_ratio >= 0.50 and text_area_ratio <= 0.12:
            kind = "mixed"
        elif image_area_ratio >= 0.15 and text_area_ratio >= 0.01:
            kind = "mixed"
        else:
            kind = "text"

    return {
        "page": page_number,
        "kind": kind,
        "hasSelectableText": has_selectable_text,
        "wordCount": word_count,
        "textBlockCount": text_block_count,
        "imageBlockCount": image_block_count,
        "textAreaRatio": round(text_area_ratio, 6),
        "imageAreaRatio": round(image_area_ratio, 6),
    }


def analyze_document(input_path: str, password: Optional[str] = None) -> Dict[str, Any]:
    doc = open_document(input_path, password)
    try:
        pages: List[Dict[str, Any]] = []
        for i in range(doc.page_count):
            pages.append(analyze_page(doc[i], i + 1))

        return {
            "pageCount": doc.page_count,
            "pages": pages,
        }
    finally:
        doc.close()


def main() -> int:
    if len(sys.argv) < 2:
        print("Usage: pdf_analyzer.py <input.pdf> [password]", file=sys.stderr)
        return 1

    input_path = sys.argv[1]
    password = sys.argv[2] if len(sys.argv) > 2 else None

    input_file = Path(input_path)
    if not input_file.exists():
        print(f"Input PDF not found: {input_path}", file=sys.stderr)
        return 1

    try:
        result = analyze_document(str(input_file), password)
        print(json.dumps(result, ensure_ascii=False))
        return 0
    except Exception as e:
        print(str(e), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())