import { defineConfig, defineDocs } from 'fumadocs-mdx/config';
import { metaSchema, pageSchema } from 'fumadocs-core/source/schema';
import { remarkMdxMermaid } from 'fumadocs-core/mdx-plugins';

// You can customize Zod schemas for frontmatter and `meta.json` here
// see https://fumadocs.dev/docs/mdx/collections
export const docs = defineDocs({
  dir: 'content/docs',
  docs: {
    schema: pageSchema,
    postprocess: {
      includeProcessedMarkdown: true,
    },
  },
  meta: {
    schema: metaSchema,
  },
});

/* ───────────────────────────────────────────────────────────
   code-runner syntax themes — instrument-panel monochrome.

   A near-achromatic ink ramp carries structure; the single
   electric-iris accent is rationed onto the tokens that signal
   control flow (keywords, tags, attributes). No rainbow, no
   second chromatic hue — code reads like the rest of the system:
   precise, hardened, quiet. Both hold WCAG AA on the --section
   code surface. Source of truth: DESIGN.md.
   ─────────────────────────────────────────────────────────── */

const crLight = {
  name: 'cr-light',
  type: 'light' as const,
  colors: { 'editor.background': '#f6f7f9', 'editor.foreground': '#26282d' },
  settings: [
    { settings: { foreground: '#26282d' } },
    {
      scope: ['comment', 'punctuation.definition.comment', 'string.comment'],
      settings: { foreground: '#6c6f77', fontStyle: 'italic' },
    },
    {
      scope: [
        'keyword',
        'storage',
        'storage.type',
        'storage.modifier',
        'keyword.control',
        'keyword.operator.expression',
        'keyword.operator.new',
        'keyword.operator.logical',
        'variable.language',
      ],
      settings: { foreground: '#4f46e5' },
    },
    {
      scope: [
        'entity.name.function',
        'support.function',
        'meta.function-call',
        'meta.function-call.method',
      ],
      settings: { foreground: '#0a0a0b', fontStyle: 'bold' },
    },
    {
      scope: [
        'entity.name.type',
        'entity.name.class',
        'support.type',
        'support.class',
        'entity.other.inherited-class',
      ],
      settings: { foreground: '#0a0a0b', fontStyle: 'bold' },
    },
    {
      scope: ['entity.name.tag', 'entity.other.attribute-name', 'variable.parameter'],
      settings: { foreground: '#4f46e5' },
    },
    {
      scope: ['string', 'string.quoted', 'punctuation.definition.string', 'string.template'],
      settings: { foreground: '#565961' },
    },
    {
      scope: ['constant.numeric', 'constant.language', 'support.constant', 'constant.character'],
      settings: { foreground: '#0a0a0b' },
    },
    {
      scope: ['punctuation', 'meta.brace', 'keyword.operator', 'punctuation.separator'],
      settings: { foreground: '#8a8d94' },
    },
  ],
};

const crDark = {
  name: 'cr-dark',
  type: 'dark' as const,
  colors: { 'editor.background': '#0f1114', 'editor.foreground': '#c4c7ce' },
  settings: [
    { settings: { foreground: '#c4c7ce' } },
    {
      scope: ['comment', 'punctuation.definition.comment', 'string.comment'],
      settings: { foreground: '#787c85', fontStyle: 'italic' },
    },
    {
      scope: [
        'keyword',
        'storage',
        'storage.type',
        'storage.modifier',
        'keyword.control',
        'keyword.operator.expression',
        'keyword.operator.new',
        'keyword.operator.logical',
        'variable.language',
      ],
      settings: { foreground: '#8b82ff' },
    },
    {
      scope: [
        'entity.name.function',
        'support.function',
        'meta.function-call',
        'meta.function-call.method',
      ],
      settings: { foreground: '#f4f5f7', fontStyle: 'bold' },
    },
    {
      scope: [
        'entity.name.type',
        'entity.name.class',
        'support.type',
        'support.class',
        'entity.other.inherited-class',
      ],
      settings: { foreground: '#f4f5f7', fontStyle: 'bold' },
    },
    {
      scope: ['entity.name.tag', 'entity.other.attribute-name', 'variable.parameter'],
      settings: { foreground: '#8b82ff' },
    },
    {
      scope: ['string', 'string.quoted', 'punctuation.definition.string', 'string.template'],
      settings: { foreground: '#969aa3' },
    },
    {
      scope: ['constant.numeric', 'constant.language', 'support.constant', 'constant.character'],
      settings: { foreground: '#f4f5f7' },
    },
    {
      scope: ['punctuation', 'meta.brace', 'keyword.operator', 'punctuation.separator'],
      settings: { foreground: '#6b6f78' },
    },
  ],
};

export default defineConfig({
  mdxOptions: {
    // ```mermaid code blocks → <Mermaid /> (registered in components/mdx.tsx)
    remarkPlugins: [remarkMdxMermaid],
    rehypeCodeOptions: {
      themes: { light: crLight, dark: crDark },
    },
  },
});
