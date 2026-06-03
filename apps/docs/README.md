# code-runner docs

The documentation site for [code-runner](../../README.md), built with
[Fumadocs](https://fumadocs.dev) on Next.js.

## Develop

```bash
pnpm install      # from the repo root
pnpm --filter docs dev
```

Open http://localhost:3000.

## Build

```bash
pnpm --filter docs build
```

The build fully compiles every MDX page, so a green build means the docs are valid.

## Where things live

| Path | What |
| --- | --- |
| `content/docs/**` | All documentation content (MDX). Navigation is driven by `meta.json` files. |
| `app/(home)/page.tsx` | The marketing landing page. |
| `lib/shared.ts` | App name, GitHub repo, and route constants. |
| `lib/layout.shared.tsx` | Shared nav/layout options (top-bar links, GitHub URL). |
| `lib/source.ts` | Fumadocs content source adapter. |
| `source.config.ts` | Fumadocs MDX collection config + frontmatter schema. |

## Editing content

- Add a page: drop a `.mdx` file under `content/docs/` with `title` and `description`
  frontmatter, and add its slug to the nearest `meta.json` `pages` array.
- Add a section: create a folder with its own `meta.json` (`title`, optional lucide
  `icon`, and ordered `pages`).
- Components available in MDX: `Cards`/`Card` and `Callout` (default), plus
  `Tabs`/`Tab` and `Steps`/`Step` via `import { ... } from 'fumadocs-ui/components/...'`.
- Frontmatter `icon` accepts any [lucide](https://lucide.dev/icons) icon name.

Content is the source of truth — keep it in sync with the code it documents.
