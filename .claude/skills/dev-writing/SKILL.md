---
name: dev-writing
description: "Write or edit agnt docs-site articles/guides — SEO template, honesty rules, and AI-tell elimination. Use when: write article, dev site guide, docs-site page, problem-solution page, edit guide prose"
---

# Dev-Site Article Writing

How to write agnt docs-site content. Distilled from `docs/plans/2026-02-18-seo-content-strategy-design.md` (the article strategy the existing guides were written from) plus the honesty rules in `.claude/rules/publish-security-review-lessons.md`. Three parts: structure, honesty, and voice — the voice section is the eliminate-AI-tells checklist and applies to every sentence.

## Structure (problem-solution pages, `docs-site/docs/guides/`)

1000-1500 words. Five parts, in order:

1. **The problem** — open inside the reader's experience of the bug/pain. No preamble, no restating the title.
2. **The traditional approach** — the tools people reach for and precisely where each goes blind. Name real tools (DevTools panels, Lighthouse, React DevTools).
3. **The agnt approach** — before/after with concrete tool calls. Show the actual JSON call shapes and real output.
4. **Step-by-step walkthrough** — reproducible; a reader can paste their way through. Include a simulated/demo bug when the issue class needs one.
5. **See Also** — internal links (every page also links 2-3 related pages inside the body text).

Frontmatter, every page:

```yaml
---
title: "Title Containing the Target Keyword"
description: "150-160 chars, primary keyword naturally included"
keywords: [the phrases people actually search, long-tail included]
sidebar_label: "Short Label"
---
```

Register the page in `docs-site/sidebars.ts` and build (`tman run -- npm run build` in `docs-site/`) before calling it done — the build is the link checker.

## Honesty rules

- **Only document what ships.** Before naming a function, tool param, output field, or format: read the source (`internal/proxy/scripts/`, `internal/tools/`) and confirm it exists with that exact name and shape. Sample outputs must match the real formatter, not an idealized one.
- **Coverage words are load-bearing.** "every", "all", "automatically" — count the call sites before writing them. One false capability claim obliges a sweep of the whole page for the same shape.
- **Name the limits.** If an instrument has a blind spot (rAF sampling on backgrounded tabs, CORS-blocked stylesheets, a number only the human can read), the article says so where it teaches the instrument — a documented limit builds trust; a discovered one destroys it.
- **Real numbers from real runs.** When a walkthrough shows output, generate it by actually running the code where feasible, and say when a value is illustrative.

## Voice — eliminate AI tells

The articles should read like an experienced engineer explaining a war story, not like generated content. Each item below is a tell that marks prose as machine-written; remove them at writing time, not in review.

**Banned vocabulary**: delve, leverage (as a verb), robust, seamless, seamlessly, supercharge, game-changer, revolutionize, unlock, elevate, empower, harness (as a verb), "in today's fast-paced world", "ever-evolving landscape", "it's important to note", "it's worth noting", "let's dive in", "in conclusion", "to summarize".

**Banned constructions**:
- "It's not just X — it's Y." / "This isn't about X. It's about Y." Say the actual claim once, directly.
- Rule-of-three adjective or noun triads used rhythmically ("fast, reliable, and scalable"). Lists of three are fine when there are actually three things.
- A closing paragraph that restates what the article just said. End on the last piece of information, a limit, or a pointer to the next thing — not a summary.
- An opening paragraph that defines the topic generically ("Performance is critical for modern web applications"). Open inside the specific problem.
- Rhetorical-question openers ("Ever wondered why...?").
- Em-dash chains and arrow chains (`A → B → fails`) in prose. Full sentences; dashes at most once per paragraph.
- Every-paragraph-starts-with-a-bolded-label formatting. Use bold labels only in genuine reference lists.
- Hedging stacks ("can potentially help to..."). Commit: it does or it doesn't.

**Positive markers** (what human technical prose has):
- Varied sentence length, including short ones.
- Concrete nouns: file paths, function names, port numbers, real error text — not "various issues" or "certain scenarios".
- Opinions with reasons ("`animation-play-state: paused` rather than `animation: none` — pausing freezes the compositor work without collapsing layout").
- Occasional dry asides where earned; never forced humor.
- Second person for the reader's actions, first person plural sparingly or not at all. No "we're excited".

**Self-check before finishing**: read the first sentence of every paragraph in sequence — if that skeleton alone reads like a listicle or a press release, restructure. Grep the draft for the banned vocabulary. Check every quantifier against the code.
