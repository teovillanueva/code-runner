# Product

## Register

brand

> Hybrid surface. The marketing landing (`/`) is **brand** — design IS the product there: it has to win a skeptical developer's trust in seconds. The reference pages (`/docs/*`) are **product** — design SERVES the content: readability and code legibility lead. A hosted SaaS product is planned for the future and will be **product** register. Pick register per-surface; this file defaults to `brand` because the landing is the conversion and trust moment.

## Users

Developers and platform engineers evaluating or operating an internal code-execution service. Two contexts:

- **Evaluators** land on `/` from GitHub, Hacker News, or a search, deciding in under a minute whether this is trustworthy infrastructure to run *untrusted* code on. They are technical, skeptical, and allergic to marketing. They want to see the security model, the architecture, and a copy-pasteable quickstart — fast.
- **Operators / integrators** live in `/docs/*` while self-hosting: wiring `docker compose`, configuring env-based auth, adding a language, deploying to Fly.io, integrating the Node/React SDKs. Their job is to get a working, hardened deployment and to extend it without touching the core.

The job to be done: *decide this is safe and competent, then get it running.*

## Product Purpose

code-runner is an open-source (MIT), self-hostable, Piston-style remote code execution service: it runs untrusted code in a hardened, resource-bounded sandbox with live interactive stdin and real-time output streaming. The docs site exists to (1) earn developer trust in the security and reliability model, and (2) make self-hosting and extending it trivial.

Success looks like: an evaluator reads the landing and thinks "these people understand sandboxing"; an operator goes from clone to a working interactive round-trip without leaving the docs; adding a language feels like dropping in a folder, exactly as promised.

## Brand Personality

**Sharp, modern, confident.** Three words: *precise, hardened, fast.*

Voice is the voice of someone who has actually run untrusted code in production and respects the reader's intelligence — concrete nouns and verbs, no hedging, no hype. It shows the security model rather than claiming to be "secure." Terminal-native and systems-software literate without cosplaying as a retro terminal. Quietly opinionated: it made specific stack choices and can defend each one.

Emotional goal: **earned trust through evident competence.** The reader should feel they're looking at infrastructure built by people who sweat the threat model, not a weekend project with a nice README.

## Anti-references

- **Generic SaaS landing.** No gradient-blob hero, no pastel mesh, no "hero metric" template (big number / small label / supporting stats), no identical icon-card feature grid repeated down the page, no buzzword copy (streamline / empower / supercharge / seamless / enterprise-grade).
- **Default Fumadocs / Vercel-clone docs.** It must not look like every other Next.js docs site running stock `neutral.css` with no identity. The current landing leans on default `fd-primary` tokens and an identical 6-cell card grid — that is precisely the look to break away from. Earn a distinct, ownable visual identity.
- Not enterprise/corporate (navy-and-gray, stock photography, heavy and slow).
- Not cutesy/playful (mascots, candy colors, rounded blobs, illustration-heavy). Too soft for security infrastructure.

## Design Principles

1. **Show the machinery.** Trust comes from evidence, not adjectives. Lead with the real threat model, the real architecture, real copy-pasteable commands. Concrete over claimed.
2. **Code is a first-class citizen.** This is a tool developers read code to evaluate. Code blocks, terminal output, and the wire contract deserve typographic and visual care equal to prose — legible, syntax-aware, easy to copy.
3. **Earned confidence, no swagger.** Opinionated and precise, never loud or salesy. Restraint reads as competence to this audience; overselling reads as insecurity.
4. **Fast and weightless.** The product's promise is real-time and low-latency; the site must embody it. Quick loads, instant navigation, motion that's purposeful and snappy (ease-out, no bounce), nothing decorative that costs a frame.
5. **Extensible by design, visibly.** "Add a language = add a folder" is the core extensibility story; the docs structure and the add-a-language path should make that simplicity self-evident, not buried.

## Accessibility & Inclusion

Target **WCAG 2.2 AA**: ≥4.5:1 contrast for body text (and placeholders), ≥3:1 for large text and meaningful UI; full keyboard navigation with visible focus; semantic structure and landmarks. Every animation needs a `prefers-reduced-motion: reduce` alternative (crossfade or instant). Don't rely on color alone to convey state (relevant for any status/security indicators); pair with text or icon. Maintain AA contrast in both light and dark themes, since a dev-tool docs site will be read in both.
