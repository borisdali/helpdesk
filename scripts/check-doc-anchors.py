#!/usr/bin/env python3
"""Fail on broken relative links and #anchors in docs/*.md, README.md and deploy READMEs.

Anchors follow GitHub's slug rules (lowercase, punctuation dropped, spaces to
hyphens, -N suffix for duplicates). Fenced code blocks are ignored.
Usage: python3 -I scripts/check-doc-anchors.py
"""
import glob, os, re, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = sorted(set(
    glob.glob(os.path.join(ROOT, "docs", "**", "*.md"), recursive=True)
    + [os.path.join(ROOT, "README.md")]
    + glob.glob(os.path.join(ROOT, "deploy", "**", "README.md"), recursive=True)
))
LINK = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEAD = re.compile(r"^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$")


def slug(text):
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)
    text = re.sub(r"[`*]", "", text)
    text = text.strip().lower()
    text = re.sub(r"[^\w\- ]", "", text, flags=re.UNICODE)
    return text.replace(" ", "-")


def scan(path):
    anchors, links, seen, fence = set(), [], {}, False
    for n, line in enumerate(open(path, encoding="utf-8"), 1):
        if line.lstrip().startswith(("```", "~~~")):
            fence = not fence
            continue
        if fence:
            continue
        m = HEAD.match(line)
        if m:
            s = slug(m.group(2))
            k = seen.get(s, 0)
            seen[s] = k + 1
            anchors.add(s if k == 0 else f"{s}-{k}")
        for t in re.findall(r'<a\s+(?:name|id)="([^"]+)"', line):
            anchors.add(t)
        stripped = re.sub(r"`[^`]*`", "", line)
        for target in LINK.findall(stripped):
            links.append((n, target))
    return anchors, links


cache = {}
def info(p):
    if p not in cache:
        cache[p] = scan(p)
    return cache[p]


bad = 0
for f in FILES:
    _, links = info(f)
    for n, t in links:
        if t == "..." or re.match(r"[a-z][a-z0-9+.-]*:", t) or t.startswith("//"):  # "..." = unpublished blog placeholder
            continue
        path, _, frag = t.partition("#")
        dest = f if path == "" else os.path.normpath(os.path.join(os.path.dirname(f), path))
        rel = os.path.relpath(f, ROOT)
        if not os.path.exists(dest):
            print(f"{rel}:{n}: missing file {t}"); bad += 1; continue
        if frag and dest.endswith(".md") and os.path.isfile(dest):
            if frag.lower() not in {a.lower() for a in info(dest)[0]}:
                print(f"{rel}:{n}: no anchor #{frag} in {os.path.relpath(dest, ROOT)}"); bad += 1
if bad:
    print(f"{bad} broken link(s)"); sys.exit(1)
print(f"checked {len(FILES)} files: all links resolve")
