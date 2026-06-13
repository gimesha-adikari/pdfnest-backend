# file: scripts/pdf_to_html.py
import fitz  # PyMuPDF
import sys
import json
import base64

def extract_to_clean_html(input_pdf_path):
    try:
        doc = fitz.open(input_pdf_path)
        html_blocks = []

        # Base styles to look like a standard document layout sheet inside the editor
        html_blocks.append("<style>")
        html_blocks.append(".editor-page { background: white; margin-bottom: 30px; box-shadow: 0 4px 6px rgba(0,0,0,0.05); padding: 2cm; min-height: 297mm; }")
        html_blocks.append("p { margin-bottom: 1em; font-family: sans-serif; font-size: 14px; line-height: 1.6; color: #333; }")
        html_blocks.append("h1, h2, h3 { font-family: sans-serif; color: #111; margin-top: 1.2em; margin-bottom: 0.5em; }")
        html_blocks.append("img { max-width: 100%; height: auto; display: block; margin: 15px auto; }")
        html_blocks.append("</style>")

        for page_idx, page in enumerate(doc):
            html_blocks.append(f"<div class='editor-page' data-page='{page_idx + 1}'>")

            # Extract structured text and image blocks from the document layout matrix
            text_page = page.get_text("dict")

            for block in text_page["blocks"]:
                # Type 0: This is a Text block
                if block["type"] == 0:
                    paragraph_text = ""
                    is_heading = False

                    for line in block["lines"]:
                        line_text = ""
                        for span in line["spans"]:
                            text_content = span["text"]
                            # Detect if this block is likely a heading based on font size attributes
                            if span["size"] > 18:
                                is_heading = True
                            line_text += text_content

                        if line_text.strip():
                            paragraph_text += line_text + " "

                    if paragraph_text.strip():
                        if is_heading:
                            html_blocks.append(f"<h2>{paragraph_text.strip()}</h2>")
                        else:
                            html_blocks.append(f"<p>{paragraph_text.strip()}</p>")

                # Type 1: This is an Image block
                elif block["type"] == 1:
                    try:
                        image_bytes = block["image"]
                        # Encode image data directly into standard inline base64 HTML strings
                        image_base64 = base64.b64encode(image_bytes).decode('utf-8')
                        image_ext = block["ext"]
                        html_blocks.append(f'<img src="data:image/{image_ext};base64,{image_base64}" />')
                    except Exception:
                        pass # Gracefully bypass empty or un-extractable image stream chunks

            html_blocks.append("</div>")

        doc.close()

        # Combine everything into a single layout document string
        final_html = "\n".join(html_blocks)
        print(json.dumps({"success": True, "html": final_html}))

    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    if len(sys.argv) > 1:
        extract_to_clean_html(sys.argv[1])