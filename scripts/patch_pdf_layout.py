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
        return   (1.0, 1.0, 1.0)


def patch_pdf(input_pdf_path, output_pdf_path, pages_json_path):
    try:
        # 1. Read original document file instance to track rotation properties
        orig_doc = fitz.open(input_pdf_path)
        rotations = [p.rotation for p in orig_doc]

        with open(pages_json_path, 'r', encoding='utf-8') as f:
            layout_data = json.load(f)

        # 2. Open our working upright document copy
        upright_path = layout_data.get("upright_tracker", input_pdf_path)
        source_doc = fitz.open(upright_path)

        # Initialize a clean blank output container to break free from hidden duplicate streams
        output_doc = fitz.open()

        for page_idx, page_data in enumerate(layout_data.get("pages", [])):
            if page_idx >= len(source_doc):
                continue

            source_page = source_doc[page_idx]
            elements = page_data.get("elements", [])

            # ✅ Fix: Mask old text directly on the source page FIRST so it isn't captured in the image
            for element in elements:
                x0, y0 = element.get("x"), element.get("y")
                w, h = element.get("width"), element.get("height")
                bg_hex = element.get("bg_color", "#ffffff")

                if x0 is None or y0 is None or w is None or h is None:
                    continue

                color_rgb = hex_to_rgb(bg_hex)
                rect = fitz.Rect(x0 - 1, y0 - 1, x0 + w + 2, y0 + h + 1)
                source_page.draw_rect(rect, color=color_rgb, fill=color_rgb, overlay=True)

            # Create a completely blank target page container
            new_page = output_doc.new_page(
                width=source_page.rect.width,
                height=source_page.rect.height
            )

            # ✅ Take a snapshot of the masked source page (clean background, no duplicated text)
            pix = source_page.get_pixmap(matrix=fitz.Matrix(2.0, 2.0))
            new_page.insert_image(new_page.rect, pixmap=pix)

            # ✅ Insert your updated editable input changes clearly on top of the clean snapshot
            for element in elements:
                x0, y0 = element.get("x"), element.get("y")
                h = element.get("height")
                text_val = element.get("text", "")
                text_color_hex = element.get("text_color", "#000000")

                if x0 is None or y0 is None or h is None or not text_val.strip():
                    continue

                pt = fitz.Point(x0, y0 + (h * 0.85))
                font_rgb = hex_to_rgb(text_color_hex)

                new_page.insert_text(
                    pt,
                    text_val,
                    fontsize=element.get("size", 11),
                    fontname="helv",
                    color=font_rgb
                )

        # 3. Restore native rotation angles right before saving
        for idx, page in enumerate(output_doc):
            if idx < len(rotations):
                page.set_rotation(rotations[idx])

        output_doc.save(output_pdf_path)
        output_doc.close()
        source_doc.close()
        orig_doc.close()

        print(json.dumps({"success": True}))
    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) > 3:
        patch_pdf(sys.argv[1], sys.argv[2], sys.argv[3])