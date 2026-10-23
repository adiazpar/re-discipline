---
name: open-campaign
description: >-
  Open a re-discipline campaign: an investigation workspace for one
  task. Creates the campaign folder and records the goal in-session.
---

CLI examples use `re-search`; on Windows use `re-search.exe`.

# Open A Campaign

1. **Search first.** Run
   `.re-discipline/bin/re-search query "<the goal>"` — existing
   docs may already answer part or all of it. Cite what you find.
2. Pick a short kebab-case slug (e.g. `demon-transforms`).
3. Create:

```
.re-discipline/active/<slug>/
  CAMPAIGN.md
  reports/
  findings/
```

(`work/` — campaign-scoped scratch for scripts, dumps, and logs — is
created on demand when the investigation needs it; see CONVENTIONS.md's
"Working artifacts" section.)

4. Write `CAMPAIGN.md`:

```markdown
# <Campaign title>

**Goal:** <one paragraph: what we want to achieve or learn>
**Opened:** <date>
**Status:** open

## Working notes
<running log: theories, leads, decisions — updated as the campaign runs>
```

Goals live here and in the session — there is no separate goal document
type. One campaign per concurrent session/task keeps sessions from
colliding. Follow `.re-discipline/CONVENTIONS.md` (or the curate skill)
for how reports and findings are written.
