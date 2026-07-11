#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
from pathlib import Path

import fitz  # PyMuPDF
from PIL import Image


def render_page(document_path: str, page_number: int, dpi: float) -> dict:
    doc = fitz.open(document_path)
    try:
        if page_number < 1 or page_number > doc.page_count:
            return {
                "success": False,
                "error": f"Invalid page {page_number}; document has {doc.page_count} pages",
            }

        page = doc[page_number - 1]
        zoom = float(dpi) / 72.0
        mat = fitz.Matrix(zoom, zoom)
        pix = page.get_pixmap(matrix=mat, alpha=False)

        base = Path(document_path).with_suffix("")
        image_path = str(base.parent / f"{base.name}-page-{page_number}.jpg")

        img = Image.frombytes("RGB", [pix.width, pix.height], pix.samples)
        if img.mode != "RGB":
            img = img.convert("RGB")
        img.save(image_path, format="JPEG", quality=85, optimize=True)

        return {"success": True, "imagePath": image_path}
    finally:
        doc.close()


def main() -> int:
    req = json.load(sys.stdin)

    document_path = req["documentPath"]
    page = int(req["page"])
    dpi = float(req.get("dpi", 144))

    result = render_page(document_path, page, dpi)
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result.get("success") else 1


if __name__ == "__main__":
    raise SystemExit(main())