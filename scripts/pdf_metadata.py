#!/usr/bin/env python3
import argparse
import json
import os
import sys

import fitz  # PyMuPDF


def open_document(path: str, password: str):
    doc = fitz.open(path)
    if doc.needs_pass:
        if not password:
            doc.close()
            raise RuntimeError("password required for encrypted PDF")
        if not doc.authenticate(password):
            doc.close()
            raise RuntimeError("invalid PDF password")
    return doc


def read_metadata(input_path: str, password: str) -> None:
    doc = open_document(input_path, password)
    try:
        meta = doc.metadata or {}
        result = {
            "title": (meta.get("title") or "").strip(),
            "author": (meta.get("author") or "").strip(),
            "subject": (meta.get("subject") or "").strip(),
            "keywords": (meta.get("keywords") or "").strip(),
        }
        print(json.dumps(result, ensure_ascii=False))
    finally:
        doc.close()


def write_metadata(input_path: str, output_path: str, password: str, metadata_json: str) -> None:
    doc = open_document(input_path, password)
    try:
        incoming = json.loads(metadata_json or "{}")

        meta = doc.metadata or {}
        meta["title"] = str(incoming.get("title", "")).strip()
        meta["author"] = str(incoming.get("author", "")).strip()
        meta["subject"] = str(incoming.get("subject", "")).strip()
        meta["keywords"] = str(incoming.get("keywords", "")).strip()

        doc.set_metadata(meta)
        doc.save(output_path)
    finally:
        doc.close()


def main() -> int:
    parser = argparse.ArgumentParser(description="Read or update PDF metadata")
    sub = parser.add_subparsers(dest="command", required=True)

    read_p = sub.add_parser("read")
    read_p.add_argument("input")
    read_p.add_argument("--password", default="")

    write_p = sub.add_parser("write")
    write_p.add_argument("input")
    write_p.add_argument("output")
    write_p.add_argument("--password", default="")
    write_p.add_argument("--metadata-json", required=True)

    args = parser.parse_args()

    try:
        if args.command == "read":
            read_metadata(args.input, args.password)
        elif args.command == "write":
            write_metadata(args.input, args.output, args.password, args.metadata_json)
        else:
            raise RuntimeError("unknown command")
        return 0
    except Exception as e:
        print(str(e), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())