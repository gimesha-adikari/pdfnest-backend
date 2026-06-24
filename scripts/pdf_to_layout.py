import fitz
import sys
import json
import os


def get_pixel_bg_hex(page, x, y):
    try:
        sample_rect = fitz.Rect(x, y, x + 2, y + 2)
        pix = page.get_pixmap(clip=sample_rect, matrix=fitz.Matrix(1, 1))
        if pix.samples:
            r, g, b = pix.samples[0], pix.samples[1], pix.samples[2]
            return f"#{r:02x}{g:02x}{b:02x}"
        return "#ffffff"
    except Exception:
        return "#ffffff"


def extract_pdf_layout(input_pdf_path):
    try:
        doc = fitz.open(input_pdf_path)
        pages_data = []

        # 1. First Pass: Reset all pages to 0 rotation to unroll embedded orientations
        for page in doc:
            page.set_rotation(0)

        # 2. Save an upright variant that the frontend canvas will consume cleanly
        base, ext = os.path.splitext(input_pdf_path)
        upright_pdf_path = base + "_upright" + ext
        doc.save(upright_pdf_path)

        # 3. Second Pass: Track structural text lines from the upright document variant safely
        for page_idx, page in enumerate(doc):
            page_rect = page.rect

            page_data = {
                "page_num": page_idx + 1,
                "width": page_rect.width,
                "height": page_rect.height,
                "elements": []
            }

            text_page = page.get_text("dict")
            blocks = text_page.get("blocks", [])

            for block in blocks:
                if block.get("type") == 0:
                    lines = block.get("lines", [])
                    for line in lines:
                        bbox = line.get("bbox")
                        if not bbox or len(bbox) < 4:
                            continue
                        x0, y0, x1, y1 = bbox

                        bg_hex = get_pixel_bg_hex(page, x0 + 1, y0 + 1)

                        line_text = ""
                        font_size = 12
                        font_name = "sans-serif"
                        text_color_hex = "#000000"

                        spans = line.get("spans", [])
                        if spans:
                            first_span = spans[0]
                            if "color" in first_span:
                                try:
                                    c_int = first_span["color"]
                                    r = ((c_int >> 16) & 255) / 255.0
                                    g = ((c_int >> 8) & 255) / 255.0
                                    b = (c_int & 255) / 255.0
                                    text_color_hex = f"#{int(r * 255):02x}{int(g * 255):02x}{int(b * 255):02x}"
                                except Exception:
                                    text_color_hex = "#000000"

                        for span in spans:
                            line_text += span.get("text", "")
                            font_size = span.get("size", font_size)
                            font_name = span.get("font", font_name)

                        if line_text.strip():
                            page_data["elements"].append({
                                "text": line_text,
                                "x": x0,
                                "y": y0,
                                "width": x1 - x0,
                                "height": y1 - y0,
                                "size": font_size,
                                "font": font_name,
                                "bg_color": bg_hex,
                                "text_color": text_color_hex
                            })
            pages_data.append(page_data)

        doc.close()
        print(json.dumps({"success": True, "pages": pages_data, "upright_tracker": upright_pdf_path}))

    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) > 1:
        extract_pdf_layout(sys.argv[1])