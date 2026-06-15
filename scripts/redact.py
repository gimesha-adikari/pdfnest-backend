import fitz
import sys
import json

def secure_redact(input_pdf, output_pdf, keywords, boxes_json):
    try:
        doc = fitz.open(input_pdf)
        drawn_boxes = json.loads(boxes_json)

        for page_idx, page in enumerate(doc):
            page_num = page_idx + 1

            for keyword in keywords:
                keyword = keyword.strip()
                if not keyword:
                    continue

                text_instances = page.search_for(keyword)
                for inst in text_instances:
                    page.add_redact_annot(inst, fill=(0, 0, 0))

            page_boxes = [b for b in drawn_boxes if int(b["page"]) == page_num]
            page_rect = page.rect
            w = page_rect.width
            h = page_rect.height

            for box in page_boxes:
                x0 = float(box["x"]) * w
                y0 = float(box["y"]) * h
                x1 = x0 + (float(box["width"]) * w)
                y1 = y0 + (float(box["height"]) * h)

                rect_target = fitz.Rect(x0, y0, x1, y1)
                page.add_redact_annot(rect_target, fill=(0, 0, 0))

            page.apply_redactions()

        doc.save(output_pdf)
        doc.close()
        print(json.dumps({"success": True}))

    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    input_path = sys.argv[1]
    output_path = sys.argv[2]
    keywords = sys.argv[3].split("|||")
    boxes_json = sys.argv[4]

    secure_redact(input_path, output_path, keywords, boxes_json)