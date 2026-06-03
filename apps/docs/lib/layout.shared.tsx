import type { BaseLayoutProps, LinkItemType } from 'fumadocs-ui/layouts/shared';
import { Logomark } from '@/components/logomark';
import { appName, gitConfig } from './shared';

/**
 * Shared nav identity (logo + GitHub) for every layout.
 *
 * `links` are intentionally NOT included here. In the docs layout Fumadocs
 * renders top-level `links` *inside the sidebar*, above the page tree — which
 * duplicated tree entries (a second "Quickstart") and left a "Documentation"
 * item permanently highlighted via `active: 'nested-url'`. The sidebar tree is
 * the canonical docs navigation, so docs gets no extra links; the marketing
 * header opts in via `navLinks` below.
 */
export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <span className="flex items-center gap-2.5">
          <Logomark className="h-[15px] w-auto text-ink" />
          <span className="font-mono text-[15px] font-medium tracking-tight">
            {appName}
          </span>
        </span>
      ),
    },
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}

/** Header links for the marketing layout only (never the docs sidebar). */
export const navLinks: LinkItemType[] = [
  { text: 'Quickstart', url: '/docs/quickstart' },
  { text: 'Documentation', url: '/docs' },
];
