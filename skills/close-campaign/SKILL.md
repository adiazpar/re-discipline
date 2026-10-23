---
name: close-campaign
description: >-
  Close a re-discipline campaign: promote remaining findings, write the
  summary, archive the folder. A file move, not a proof obligation.
---

CLI examples use `re-search`; on Windows use `re-search.exe`.

# Close A Campaign

1. **Final promotion sweep** over `active/<slug>/findings/`. For each
   candidate, skim with the checklist: atomic claim? evidence cited?
   grade matches evidence? Then:
   - search `docs/` for duplicates (`re-search query`) and merge instead
     of duplicating;
   - conflicts: higher evidence grade wins, or record both with the
     conflict noted;
   - accept → set `status: promoted`, move into the right `docs/`
     subfolder;
   - reject → leave it in the campaign folder (it archives with the
     campaign; nothing is destroyed).
2. Run `.re-discipline/bin/re-search index` (rebuilds the index and
   regenerates `docs/INDEX.md`).
3. Optionally run `.re-discipline/bin/re-search bench` and add any
   questions this campaign answered to `golden.jsonl`.
4. In `CAMPAIGN.md`: set `Status: closed`, add a short summary — what
   was achieved, key promoted findings, dead ends worth remembering.
5. Move `active/<slug>/` to `archive/<slug>/`.
6. Show the user the git diff of promotions for review. Do not commit
   unless asked.
