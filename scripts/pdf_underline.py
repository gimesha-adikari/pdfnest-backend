#!/usr/bin/env python3
# file: scripts/pdf_underline.py

from __future__ import annotations

import json
import sys
from pathlib import Path
from statistics import median
from typing import Optional

import fitz  # PyMuPDF
import pytesseract
from PIL import Image

OCR_CACHE = {}


def normalize_hex(hex_color: str):
    s = (hex_color or "").strip().lstrip("#")
    if len(s) != 6:
        return (0.0, 0.0, 0.0)
    r = int(s[0:2], 16) / 255.0
    g = int(s[2:4], 16) / 255.0
    b = int(s[4:6], 16) / 255.0
    return (r, g, b)


def open_document(input_path: str, password: Optional[str] = None):
    doc = fitz.open(input_path)

    if doc.needs_pass:
        if not password:
            raise RuntimeError("PDF is password protected but no password was provided")
        if doc.authenticate(password) <= 0:
            raise RuntimeError("Invalid PDF password")

    return doc


def native_words_in_rect(page, selection_rect):
    words = []
    for item in page.get_text("words"):
        if len(item) < 5:
            continue
        rect = fitz.Rect(item[0], item[1], item[2], item[3])
        text = str(item[4]).strip()
        if text and rect.intersects(selection_rect):
            words.append({"rect": rect, "text": text})
    return words


def pixmap_to_pil_image(pix: fitz.Pixmap) -> Image.Image:
    mode = "RGB"
    if pix.n == 1:
        mode = "L"
    elif pix.n >= 4:
        mode = "RGB"

    img = Image.frombytes(mode, [pix.width, pix.height], pix.samples)

    if img.mode != "RGB":
        img = img.convert("RGB")

    return img


def ocr_words_for_page(page, page_num, zoom=2.0, lang="eng"):
    if page_num in OCR_CACHE:
        return OCR_CACHE[page_num]

    pix = page.get_pixmap(matrix=fitz.Matrix(zoom, zoom), alpha=False)
    image = pixmap_to_pil_image(pix)

    data = pytesseract.image_to_data(
        image,
        output_type=pytesseract.Output.DICT,
        lang=lang,
    )

    items = []
    n = len(data.get("text", []))

    for i in range(n):
        text = str(data["text"][i]).strip()
        conf_raw = data.get("conf", ["-1"])[i]

        try:
            conf = float(conf_raw)
        except Exception:
            conf = -1.0

        if not text or conf < 0:
            continue

        left = float(data["left"][i]) / zoom
        top = float(data["top"][i]) / zoom
        width = float(data["width"][i]) / zoom
        height = float(data["height"][i]) / zoom

        rect = fitz.Rect(left, top, left + width, top + height)
        items.append({"rect": rect, "text": text, "conf": conf})

    OCR_CACHE[page_num] = items
    return items


def group_words_by_line(word_items):
    if not word_items:
        return []

    rects = [w["rect"] for w in word_items]
    heights = [max(1.0, r.height) for r in rects]
    line_tol = max(4.0, median(heights) * 0.75)

    lines = []
    for item in sorted(word_items, key=lambda w: (w["rect"].y0, w["rect"].x0)):
        rect = item["rect"]
        placed = False

        for line in lines:
            if abs(rect.y0 - line["y0"]) <= line_tol or abs(rect.y1 - line["y1"]) <= line_tol:
                line["items"].append(item)
                line["x0"] = min(line["x0"], rect.x0)
                line["x1"] = max(line["x1"], rect.x1)
                line["y0"] = min(line["y0"], rect.y0)
                line["y1"] = max(line["y1"], rect.y1)
                placed = True
                break

        if not placed:
            lines.append({
                "items": [item],
                "x0": rect.x0,
                "x1": rect.x1,
                "y0": rect.y0,
                "y1": rect.y1,
            })

    return lines


def draw_underlines(page, word_items, color):
    lines = group_words_by_line(word_items)
    for line in lines:
        x0 = line["x0"]
        x1 = line["x1"]
        y1 = line["y1"]

        thickness = max(0.8, min(2.5, (y1 - line["y0"]) * 0.12))
        underline_y = min(page.rect.height - 1.0, y1 + 1.3)

        page.draw_line(
            fitz.Point(x0, underline_y),
            fitz.Point(x1, underline_y),
            color=color,
            width=thickness,
        )


def draw_manual_underline(page, selection_rect, color):
    thickness = max(0.8, min(2.5, selection_rect.height * 0.12))
    underline_y = min(page.rect.height - 1.0, selection_rect.y1 - max(1.0, selection_rect.height * 0.08))

    page.draw_line(
        fitz.Point(selection_rect.x0, underline_y),
        fitz.Point(selection_rect.x1, underline_y),
        color=color,
        width=thickness,
    )


def underline_boxes(doc, boxes, mode):
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
        color = normalize_hex(box.get("color", "#000000"))

        if mode == "manual":
            draw_manual_underline(page, selection_rect, color)
            continue

        if mode in ("text", "smart"):
            native = native_words_in_rect(page, selection_rect)
            if native:
                draw_underlines(page, native, color)
            else:
                draw_manual_underline(page, selection_rect, color)
            continue

        if mode == "ocr":
            ocr_items = ocr_words_for_page(page, page_num)
            selected = [item for item in ocr_items if item["rect"].intersects(selection_rect)]
            if selected:
                draw_underlines(page, selected, color)
            else:
                draw_manual_underline(page, selection_rect, color)
            continue

        draw_manual_underline(page, selection_rect, color)


def main():
    if len(sys.argv) < 5:
        print(
            "Usage: pdf_underline.py <input.pdf> <output.pdf> <boxes.json> <mode> [password]",
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
        underline_boxes(doc, boxes, mode)
        doc.save(str(output_file), deflate=True, garbage=4)
    finally:
        doc.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)