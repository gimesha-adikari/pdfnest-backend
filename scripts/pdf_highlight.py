#!/usr/bin/env python3
import json
import sys
from pathlib import Path

import fitz  # PyMuPDF


def normalize_hex(hex_color: str):
    s = (hex_color or "").strip().lstrip("#")
    if len(s) != 6:
        return (1.0, 1.0, 0.0)
    r = int(s[0:2], 16) / 255.0
    g = int(s[2:4], 16) / 255.0
    b = int(s[4:6], 16) / 255.0
    return (r, g, b)


def open_document(input_path: str, password: str | None):
    doc = fitz.open(input_path)

    if doc.needs_pass:
        if not password:
            raise RuntimeError("PDF is password protected but no password was provided")
        if doc.authenticate(password) <= 0:
            raise RuntimeError("Invalid PDF password")

    return doc


def highlight_boxes(doc, boxes):
    for box in boxes:
        page_num = int(box["page"])
        if page_num < 1 or page_num > doc.page_count:
            continue

        x = float(box["x"])
        y = float(box["y"])
        width = float(box["width"])
        height = float(box["height"])

        if width <= 0 or height <= 0:
            continue

        page = doc[page_num - 1]
        rect = fitz.Rect(x, y, x + width, y + height)

        color = normalize_hex(box.get("color", "#FFFF00"))
        opacity = 0.35

        shape = page.new_shape()
        shape.draw_rect(rect)
        shape.finish(
            color=None,
            fill=color,
            fill_opacity=opacity,
            width=0,
        )
        shape.commit()


def main():
    if len(sys.argv) < 4:
        print(
            "Usage: pdf_highlight.py <input.pdf> <output.pdf> <boxes.json> [password]",
            file=sys.stderr,
        )
        sys.exit(1)

    input_path = sys.argv[1]
    output_path = sys.argv[2]
    boxes_path = sys.argv[3]
    password = sys.argv[4] if len(sys.argv) > 4 else None

    input_file = Path(input_path)
    output_file = Path(output_path)
    boxes_file = Path(boxes_path)

    if not input_file.exists():
        raise RuntimeError(f"Input PDF not found: {input_path}")
    if not boxes_file.exists():
        raise RuntimeError(f"Boxes JSON not found: {boxes_path}")

    with boxes_file.open("r", encoding="utf-8") as f:
        boxes = json.load(f)

    doc = open_document(str(input_file), password)
    try:
        highlight_boxes(doc, boxes)
        doc.save(str(output_file), deflate=True, garbage=4)
    finally:
        doc.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)