import sys
import json
from weasyprint import HTML, CSS

def compile_pdf(input_html_path, output_pdf_path):
    try:
        print_styles = CSS(string='''
            @page {
                size: A4;
                margin: 0; /* Let our extracted editor-page containers handle internal spacing */
            }
            body {
                margin: 0;
                padding: 0;
                background-color: #ffffff;
            }
            .editor-page {
                page-break-after: always;
                box-shadow: none !important;
                margin: 0 !important;
                padding: 2cm !important;
                box-sizing: border-box;
                width: 210mm;
                height: 297mm;
                position: relative;
                overflow: hidden;
            }
            .editor-page:last-child {
                page-break-after: avoid;
            }
            p {
                font-family: sans-serif;
                font-size: 11pt;
                line-height: 1.6;
                color: #222222;
                margin-top: 0;
                margin-bottom: 1rem;
            }
            h2 {
                font-family: sans-serif;
                color: #111111;
                margin-top: 1.5rem;
                margin-bottom: 0.75rem;
                font-size: 18pt;
            }
            img {
                max-width: 100%;
                height: auto;
                display: block;
                margin: 20px auto;
            }
        ''')

        with open(input_html_path, 'r', encoding='utf-8') as f:
            raw_html = f.read()

        HTML(string=raw_html).write_pdf(output_pdf_path, stylesheets=[print_styles])
        print(json.dumps({"success": True}))

    except Exception as e:
        print(json.dumps({"success": False, "error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    if len(sys.argv) > 2:
        compile_pdf(sys.argv[1], sys.argv[2])