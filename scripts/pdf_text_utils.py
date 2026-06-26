from __future__ import annotations

from statistics import median
from typing import Optional

import fitz  # PyMuPDF
import pytesseract
from PIL import Image

OCR_CACHE = {}


def normalize_hex(hex_color: str, default: str = "#FFFF00"):
    s = (hex_color or default).strip().lstrip("#")
    if len(s) != 6:
        s = default.lstrip("#")
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

    image = Image.frombytes(mode, [pix.width, pix.height], pix.samples)
    if image.mode != "RGB":
        image = image.convert("RGB")
    return image


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