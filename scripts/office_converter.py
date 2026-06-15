import sys
import os
import fitz
from io import BytesIO


def convert_to_word(pdf_path, output_path):
    from pdf2docx import Converter

    cv = Converter(pdf_path)
    cv.convert(
        output_path,
        start=0,
        end=None,
        keep_page_layout=False,
        connected_border=True,
        line_overlap_margin=0.2,
        line_margin=0.2,
        word_margin=0.2,
        bottom_margin=5.0
    )
    cv.close()


def convert_to_excel(pdf_path, output_path):

    import pdfplumber
    import pandas as pd

    strategy_strict = {
        "vertical_strategy": "lines",
        "horizontal_strategy": "lines",
        "intersection_y_tolerance": 15
    }

    strategy_loose = {
        "vertical_strategy": "text",
        "horizontal_strategy": "text",
        "snap_tolerance": 5,
        "join_tolerance": 5
    }

    with pdfplumber.open(pdf_path) as pdf:
        with pd.ExcelWriter(output_path, engine='openpyxl') as writer:
            tables_found = 0

            for i, page in enumerate(pdf.pages):
                table = page.extract_table(strategy_strict)

                if not table:
                    table = page.extract_table(strategy_loose)

                if table:
                    cleaned_table = [[cell for cell in row] for row in table if any(row)]
                    if len(cleaned_table) > 1:  # Must have header + data
                        df = pd.DataFrame(cleaned_table[1:], columns=cleaned_table[0])
                        df.to_excel(writer, sheet_name=f'Page_{i + 1}', index=False)
                        tables_found += 1

            if tables_found == 0:
                pd.DataFrame(['No tabular data detected on any page.']).to_excel(writer, sheet_name='Result',
                                                                                 index=False)


def convert_to_powerpoint(pdf_path, output_path):

    from pptx import Presentation
    from pptx.util import Pt
    from pptx.dml.color import RGBColor

    prs = Presentation()
    doc = fitz.open(pdf_path)

    for page_num in range(len(doc)):
        page = doc.load_page(page_num)

        rect = page.rect
        prs.slide_width = Pt(rect.width)
        prs.slide_height = Pt(rect.height)

        slide = prs.slides.add_slide(prs.slide_layouts[6])

        blocks = page.get_text("dict")["blocks"]

        for block in blocks:
            # TYPE 0 = TEXT BLOCK
            if block.get("type") == 0:
                for line in block["lines"]:
                    for span in line["spans"]:
                        text = span["text"].strip()
                        if not text:
                            continue

                        x0, y0, x1, y1 = span["bbox"]

                        txBox = slide.shapes.add_textbox(Pt(x0), Pt(y0), Pt(x1 - x0), Pt(y1 - y0))
                        tf = txBox.text_frame
                        tf.clear()

                        p = tf.paragraphs[0]
                        run = p.add_run()
                        run.text = text

                        run.font.size = Pt(span["size"])

                        color_int = span["color"]
                        b = color_int & 255
                        g = (color_int >> 8) & 255
                        r = (color_int >> 16) & 255
                        run.font.color.rgb = RGBColor(r, g, b)

            elif block.get("type") == 1:
                x0, y0, x1, y1 = block["bbox"]
                try:
                    img_bytes = block["image"]
                    image_stream = BytesIO(img_bytes)
                    slide.shapes.add_picture(
                        image_stream,
                        Pt(x0), Pt(y0),
                        width=Pt(x1 - x0), height=Pt(y1 - y0)
                    )
                except Exception:
                    pass

    prs.save(output_path)


if __name__ == "__main__":
    if len(sys.argv) < 4:
        print("Usage: python office_converter.py <format> <input_pdf> <output_file>")
        sys.exit(1)

    target_format = sys.argv[1]
    input_pdf = sys.argv[2]
    output_file = sys.argv[3]

    try:
        if target_format == "docx":
            convert_to_word(input_pdf, output_file)
        elif target_format == "xlsx":
            convert_to_excel(input_pdf, output_file)
        elif target_format == "pptx":
            convert_to_powerpoint(input_pdf, output_file)
        else:
            print("Unsupported format")
            sys.exit(1)

        print("SUCCESS")
    except Exception as e:
        import traceback

        print(f"ERROR: {str(e)}\n{traceback.format_exc()}")
        sys.exit(1)