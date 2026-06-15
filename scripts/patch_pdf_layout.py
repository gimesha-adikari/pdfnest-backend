import fitz
import sys
import json


def hex_to_rgb(hex_str):
    try:
        hex_str = hex_str.lstrip('#')
        lv = len(hex_str)
        rgb_int = tuple(int(hex_str[i:i + lv // 3], 16) for i in range(0, lv, lv // 3))
        return (rgb_int[0] / 255.0, rgb_int[1] / 255.0, rgb_int[2] / 255.0)
    except Exception:
        return (1.0, 1.0, 1.0)


def patch_pdf(input_pdf_path, output_pdf_path, pages_json_path):
    try:
        doc = fitz.open(input_pdf_path)

        with open(pages_json_path, 'r', encoding='utf-8') as f:
            layout_data = json.load(f)

        for page_data in layout_data.get("pages", []):
            page_num = page_data.get("page_num")
            if page_num > len(doc):
                continue

            page = doc[page_num - 1]
            elements = page_data.get("elements", [])

            for element in elements:
                x0, y0 = element.get("x"), element.get("y")
                w, h = element.get("width"), element.get("height")
                bg_hex = element.get("bg_color", "#ffffff")

                if x0 is None or y0 is None or w is None or h is None:
                    continue

                color_rgb = hex_to_rgb(bg_hex)
                rect = fitz.Rect(x0 - 2, y0 - 1, x0 + w + 4, y0 + h + 2)
                page.draw_rect(rect, color=color_rgb, fill=color_rgb, overlay=True)

            for element in elements:
                x0, y0 = element.get("x"), element.get("y")
                h = element.get("height")
                text_val = element.get("text", "")
                text_color_hex = element.get("text_color", "#000000")  # Grab true text color

                if x0 is None or y0 is None or h is None or not text_val.strip():
                    continue

                pt = fitz.Point(x0, y0 + (h * 0.85))
                font_rgb = hex_to_rgb(text_color_hex)  # Parse matching text color

                page.insert_text(
                    pt,
                    text_val,
                    fontsize=element.get("size", 11),
                    fontname="helv",
                    color=font_rgb
                )

        doc.save(output_pdf_path)
        doc.close()
        print(json.dumps({"success": True}))
    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) > 3:
        patch_pdf(sys.argv[1], sys.argv[2], sys.argv[3])