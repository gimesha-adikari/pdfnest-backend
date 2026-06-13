# file: scripts/pdf_to_layout.py
import fitz  # PyMuPDF
import sys
import json


def get_pixel_bg_hex(page, x, y):
    """Samples the exact pixel color at specific page coordinates by rendering a tiny visual patch."""
    try:
        sample_rect = fitz.Rect(x, y, x + 2, y + 2)
        pix = page.get_pixmap(clip=sample_rect, matrix=fitz.Matrix(1, 1))
        if pix.samples:
            r, g, b = pix.samples[0], pix.samples[1], pix.samples[2]
            return f"#{r:02x}{g:02x}{b:02x}"
        return "#ffffff"
    except Exception:
        return "#ffffff"


def rgb_tuple_to_hex(rgb_tuple):
    """Converts PyMuPDF's 0-1 float RGB tuple from text spans to a web HEX string."""
    if not rgb_tuple or len(rgb_tuple) < 3:
        return "#000000"
    # PyMuPDF sometimes returns a single integer for grayscale, handle both
    if isinstance(rgb_tuple, (int, float)):
        val = max(0, min(255, int(rgb_tuple * 255)))
        return f"#{val:02x}{val:02x}{val:02x}"

    r = max(0, min(255, int(rgb_tuple[0] * 255)))
    g = max(0, min(255, int(rgb_tuple[1] * 255)))
    b = max(0, min(255, int(rgb_tuple[2] * 255)))
    return f"#{r:02x}{g:02x}{b:02x}"


def extract_pdf_layout(input_pdf_path):
    try:
        doc = fitz.open(input_pdf_path)
        pages_data = []

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
                if block.get("type") == 0:  # Text block
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
                        text_color_hex = "#000000"  # Default fallback

                        spans = line.get("spans", [])
                        if spans:
                            # Read the font color metric from the first span segment directly
                            first_span = spans[0]
                            # PyMuPDF packs color as an integer encoding or sRGB color tuple
                            if "color" in first_span:
                                # Convert PyMuPDF integer color to RGB float tuple
                                float_rgb = fitz.utils.getColorList()[0]  # default
                                try:
                                    # Get RGB tuple from integer color channel
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
                                "text_color": text_color_hex  # Added structural property field
                            })
            pages_data.append(page_data)

        doc.close()
        print(json.dumps({"success": True, "pages": pages_data}))

    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) > 1:
        extract_pdf_layout(sys.argv[1])