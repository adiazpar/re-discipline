---
name: visualize
description: >-
  Draw this project's re-discipline knowledge base and the retrieval
  machine that searches it, as an interactive page traced from the live
  index. Use when someone asks to see, visualize, diagram or explain how
  the knowledge base or its search works, or wants a picture of what is
  in it.
---

CLI examples use `re-search`; on Windows use `re-search.exe`.

# Visualize The Knowledge Base

Produce one page that shows two things: what is in this project's
knowledge base, and how a question turns into ranked answers. Every
number on it must come from the live index. Do not carry over figures
from another project, and do not invent an example.

## 1. Probe the project

Run these from the project root. `re-search` is at
`.re-discipline/bin/re-search` (`re-search.exe` on Windows).

```
.re-discipline/bin/re-search stats --json
```

That is the census: document counts by kind, status and grade, symbol
count, golden-question count, index format, and the ranking constants.

If `stats` reports zero documents, stop and say the knowledge base is
empty — there is nothing to draw yet.

## 2. Choose two real questions

The page compares two questions that drive the same ranking rule in
opposite directions. Take both from this project, preferring
`.re-discipline/golden.jsonl` so they are questions the project already
cares about:

- **A concept question** in plain English, whose answer is a curated
  `kind: fact` doc.
- **An identifier lookup** — a bare name that some `kind: reference` doc
  declares in its `idents`.

If the corpus has no `reference` docs at all, use two concept questions
and say in the page that the reference penalty never fires here.

Then trace each one:

```
.re-discipline/bin/re-search explain --json "<question>"
```

Flags go BEFORE the question. Go stops parsing flags at the first
non-flag argument, so `explain "..." --json` silently prints text.

`explain` reports every candidate with its BM25 score, reference penalty,
field coverage, ordered-pair boost, final score, and rank before and after.
The final score is `raw + penalty - boost`; exact-name priority and superseded
status also affect order, so use the reported ranks rather than sorting scores.
It runs the
same ranking code as `query`, so what it says is what a search does.

## 3. Pull one real document and one real identifier

Open a doc the traces surfaced and read its frontmatter. The page shows
one worked example of a document being taken apart, and it must be a
document that exists in this project.

For the identifier-expansion panel, take an identifier this project
actually uses — from the reference doc's `idents`, or from any title —
and run `explain` on it. Its `terms` array is exactly the expansion, so
copy those, rather than working the split out by hand.

## 4. Build the page

`template.html` sits next to this file. Copy it, replace the single
`__DATA__` token with the JSON object below, replace the placeholder
`<title>` with a name for this project's page, and publish the result
as an artifact. Keep that title stable across redraws so the artifact
keeps its identity. Do not edit anything else in the template: it is
project-neutral by construction, and every project-specific word on the
page comes through this object.

```jsonc
{
  "project": "<repo or project name>",
  "stats":   { ...verbatim from `stats --json`... },
  "sample":  {                    // one real document, for the "taken apart" panel
    "path": "docs/...", "title": "...", "body": "first sentence or two",
    "idents": ["..."], "aliases": ["..."],
    "kind": "fact", "grade": "direct", "status": "promoted"
  },
  "identExample": {               // one real identifier and its expansion
    "input": "idAnimatedEntity::AttachJoint",
    "forms": ["...", "..."]       // the `terms` array from `explain`
  },
  "traces": {
    "concept": { ...verbatim from `explain --json`... },
    "ident":   { ...verbatim from `explain --json`... }
  },
  "bench": { "passed": 0, "total": 0 }   // omit entirely if you did not run bench
}
```

Trim each trace's `rows` to the candidates worth drawing: every row whose
`raw_rank` or `final_rank` is within the page size, and no more than 20
per trace. The page animates rows between the two orders, and a hundred
of them reads as noise.

Only run `bench` if the project has a `golden.jsonl` and you are willing
to wait for it; it is slow on a large corpus. Omit the `bench` key
otherwise and the page drops that figure rather than showing a zero.

## 5. Publish

Publish as an artifact titled for the project, and give the user the
link. State plainly that every figure was traced from their index, and
name the two questions the page walks through.

## Rules

- Never put a number on the page that you did not read out of the tools
  above. This is a picture of one project, and a borrowed figure makes
  it a picture of nothing.
- Never use emojis.
- If `stats` warns the index was rebuilt, that is normal — it means the
  corpus changed since the last search.
