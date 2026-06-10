![Project cover](cover.png)

# PDFNest Backend

PDFNest Backend is a Go/Fiber API for common PDF workflows: locking and unlocking PDFs, compression, page operations, conversion, OCR, watermarking, page numbering, and metadata updates.

## Features

- Lock and unlock PDFs with AES password protection
- Compress PDF files
- Merge, split, rotate, delete, and reorder PDF pages
- Add text or image watermarks
- Add page numbers
- Read and update PDF metadata
- Convert images to PDF
- Convert PDF pages to JPEG images in a ZIP archive
- Extract OCR text from PDFs
- Convert images into a text-based PDF

## Tech Stack

- Go 1.26
- Fiber v2
- pdfcpu
- gofpdf
- Ghostscript
- Tesseract OCR

## Prerequisites

Install Go and the system binaries used by the conversion/OCR endpoints.

```bash
go version
gs --version
tesseract --version
```

On Ubuntu/Debian:

```bash
sudo apt update
sudo apt install ghostscript tesseract-ocr
```

## Getting Started

Install dependencies:

```bash
go mod download
```

Run the API:

```bash
go run .
```

The server starts on:

```text
http://localhost:8080
```

All canonical routes are mounted under `/api`.

## Project Structure

```text
.
├── main.go
├── config/
├── internal/
│   ├── conversion/
│   ├── models/
│   ├── ocr/
│   ├── optimize/
│   ├── security/
│   └── structure/
├── go.mod
└── go.sum
```

## API Reference

All file-based endpoints use `multipart/form-data`.

### Security

| Method | Endpoint | Fields | Response |
| --- | --- | --- | --- |
| POST | `/api/security/lock` | `file`, `password` | Locked PDF |
| POST | `/api/security/unlock` | `file`, `password` | Unlocked PDF |

Example:

```bash
curl -X POST http://localhost:8080/api/security/lock \
  -F "file=@document.pdf" \
  -F "password=secret" \
  --output locked.pdf
```

### Optimization

| Method | Endpoint | Fields | Response |
| --- | --- | --- | --- |
| POST | `/api/optimize/compress` | `file` | Compressed PDF |

### Structure

| Method | Endpoint | Fields | Response |
| --- | --- | --- | --- |
| POST | `/api/structure/merge` | `files` repeated at least twice | Merged PDF |
| POST | `/api/structure/split` | `file`, `pages` | Trimmed PDF containing selected pages |
| POST | `/api/structure/rotate` | `file`, `rotations` | Rotated PDF |
| POST | `/api/structure/delete-pages` | `file`, `pages` | PDF with selected pages removed |
| POST | `/api/structure/reorder-pages` | `file`, `sequence` | Reordered PDF |
| POST | `/api/structure/watermark` | `file`, `text` or `watermarkImage`, optional `description` | Watermarked PDF |
| POST | `/api/structure/add-page-numbers` | `file`, optional `description` | Numbered PDF |
| POST | `/api/structure/update-metadata` | `file`, optional `password`, `title`, `author`, `subject`, `keywords` | Updated PDF |
| POST | `/api/structure/metadata/fetch` | `file`, optional `password` | Metadata JSON |

Page selections use pdfcpu-style page expressions. Common examples are `1`, `1,3,5`, or `1-4`.

Rotate expects `rotations` as a JSON object where keys are page selections and values are degrees:

```bash
curl -X POST http://localhost:8080/api/structure/rotate \
  -F "file=@document.pdf" \
  -F 'rotations={"1":90,"2-4":180}' \
  --output rotated.pdf
```

Merge example:

```bash
curl -X POST http://localhost:8080/api/structure/merge \
  -F "files=@part-1.pdf" \
  -F "files=@part-2.pdf" \
  --output merged.pdf
```

Watermark example:

```bash
curl -X POST http://localhost:8080/api/structure/watermark \
  -F "file=@document.pdf" \
  -F "text=CONFIDENTIAL" \
  -F "description=pos:c, rot:45, scale:0.5" \
  --output watermarked.pdf
```

### Conversion

| Method | Endpoint | Fields | Response |
| --- | --- | --- | --- |
| POST | `/api/conversion/to-pdf` | `images` repeated one or more times | PDF |
| POST | `/api/conversion/pdf-to-images` | `file` | ZIP archive of JPEG pages |

Example:

```bash
curl -X POST http://localhost:8080/api/conversion/to-pdf \
  -F "images=@page-1.jpg" \
  -F "images=@page-2.jpg" \
  --output images.pdf
```

### OCR

| Method | Endpoint | Fields | Response |
| --- | --- | --- | --- |
| POST | `/api/ocr/extract-text` | `file` | Plain text file |
| POST | `/api/ocr/to-text-pdf` | `images` repeated one or more times | PDF |

Example:

```bash
curl -X POST http://localhost:8080/api/ocr/extract-text \
  -F "file=@scan.pdf" \
  --output extracted.txt
```

## Notes

- The API accepts request bodies up to 100 MB.
- Temporary files are written to the operating system temp directory and removed after each request.
- PDF-to-image conversion requires `gs` from Ghostscript.
- OCR endpoints require `tesseract`.
- `package-lock.json` is not used by the Go backend.

## Development

Format the code:

```bash
go fmt ./...
```

Run tests when test files are added:

```bash
go test ./...
```
