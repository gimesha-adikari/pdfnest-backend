from __future__ import annotations

import json
import sys
from pathlib import Path

import fitz  # PyMuPDF

from pdf_text_utils import (
    group_words_by_line,
    native_words_in_rect,
    normalize_hex,
    open_document,
    ocr_words_for_page,
)


def draw_highlight_rect(page, rect, color, opacity=0.35):
    shape = page.new_shape()
    shape.draw_rect(rect)
    shape.finish(color=None, fill=color, fill_opacity=opacity, width=0)
    shape.commit()


def highlight_words(page, word_items, color):
    lines = group_words_by_line(word_items)
    for line in lines:
        rect = fitz.Rect(line["x0"], line["y0"], line["x1"], line["y1"])
        padding_x = max(0.8, rect.width * 0.02)
        padding_y = max(0.8, rect.height * 0.15)

        rect = fitz.Rect(
            max(0.0, rect.x0 - padding_x),
            max(0.0, rect.y0 - padding_y),
            min(page.rect.width, rect.x1 + padding_x),
            min(page.rect.height, rect.y1 + padding_y),
        )
        draw_highlight_rect(page, rect, color, opacity=0.35)


def highlight_boxes(doc, boxes, mode):
    mode = (mode or "smart").strip().lower()

    for box in boxes:
        page_num = int(box.get("page", 0))
        if page_num < 1 or page_num > doc.page_count:
            continue

        x = float(box.get("x", 0))
        y = float(box.get("y", 0))
        width = float(box.get("width", 0))
        height = float(box.get("height", 0))

        if width <= 0 or height <= 0:
            continue

        page = doc[page_num - 1]
        selection_rect = fitz.Rect(x, y, x + width, y + height)
        color = normalize_hex(box.get("color", "#FFFF00"))

        if mode == "manual":
            draw_highlight_rect(page, selection_rect, color, opacity=0.35)
            continue

        if mode in ("text", "smart"):
            native = native_words_in_rect(page, selection_rect)
            if native:
                highlight_words(page, native, color)
            else:
                draw_highlight_rect(page, selection_rect, color, opacity=0.35)
            continue

        if mode == "ocr":
            ocr_items = ocr_words_for_page(page, page_num)
            selected = [item for item in ocr_items if item["rect"].intersects(selection_rect)]
            if selected:
                highlight_words(page, selected, color)
            else:
                draw_highlight_rect(page, selection_rect, color, opacity=0.35)
            continue

        draw_highlight_rect(page, selection_rect, color, opacity=0.35)


def main():
    if len(sys.argv) < 5:
        print(
            "Usage: pdf_highlight.py <input.pdf> <output.pdf> <boxes.json> <mode> [password]",
            file=sys.stderr,
        )
        sys.exit(1)

    input_path = sys.argv[1]
    output_path = sys.argv[2]
    boxes_path = sys.argv[3]
    mode = sys.argv[4]
    password = sys.argv[5] if len(sys.argv) > 5 else ""

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
        highlight_boxes(doc, boxes, mode)
        doc.save(str(output_file), deflate=True, garbage=4)
    finally:
        doc.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)