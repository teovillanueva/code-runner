import Link from 'next/link';
import { Logomark } from '@/components/logomark';

/**
 * The page resolves into the running machine. A full-bleed inverted band with
 * a continuous multiplexed worker log (the hero stream, echoed), the real docs
 * map, and the license. `.cr-on-dark` keeps iris/status legible on emphasis.
 */

const REPO = 'https://github.com/teovillanueva/code-runner';

const COLUMNS: { heading: string; links: { label: string; href: string }[] }[] = [
  {
    heading: 'Learn',
    links: [
      { label: 'Quickstart', href: '/docs/quickstart' },
      { label: 'Architecture', href: '/docs/concepts/architecture' },
      { label: 'Three clocks', href: '/docs/concepts/three-clocks' },
      { label: 'Sandbox hardening', href: '/docs/concepts/sandbox-hardening' },
    ],
  },
  {
    heading: 'Operate',
    links: [
      { label: 'Local stack', href: '/docs/self-hosting/local-stack' },
      { label: 'Configuration', href: '/docs/self-hosting/configuration' },
      { label: 'Deploy to Fly', href: '/docs/self-hosting/deploy-fly' },
      { label: 'Scaling', href: '/docs/self-hosting/scaling' },
    ],
  },
  {
    heading: 'Build',
    links: [
      { label: 'Add a language', href: '/docs/guides/add-a-language' },
      { label: 'Backend SDK', href: '/docs/guides/backend-sdk' },
      { label: 'React SDK', href: '/docs/guides/react-sdk' },
      { label: 'Wire contract', href: '/docs/api/wire-contract' },
    ],
  },
];

// a realistic multiplexed worker log — many live sessions at once
const LOG: { text: string; tone?: 'live' | 'ok' | 'warn' | 'err' | 'in' }[] = [
  { text: 'job_8f3a · python3.12 · sandbox up · network=none', tone: 'live' },
  { text: 'job_a1c4 · rust1.83  · stdin ← "12\\n"', tone: 'in' },
  { text: 'job_8f3a · python3.12 · exit 0 · 41ms cpu', tone: 'ok' },
  { text: 'job_77de · sqlite3   · slot 11/16 acquired' },
  { text: 'job_9b20 · r4.4      · idle 10s · timeout', tone: 'warn' },
  { text: 'job_a1c4 · rust1.83  · exit 0 · 263ms cpu', tone: 'ok' },
  { text: 'job_3f51 · python3.12 · cpu 15s · killed', tone: 'err' },
  { text: 'job_77de · sqlite3   · exit 0 · 8ms · reaped' },
  { text: 'job_c0a8 · r4.4      · sandbox up · ro-rootfs', tone: 'live' },
  { text: 'job_c0a8 · r4.4      · stdout → soketi · live', tone: 'in' },
];

const TONE: Record<string, string> = {
  live: 'text-status-running',
  ok: 'text-status-ok',
  warn: 'text-status-warn',
  err: 'text-status-err',
  in: 'text-emphasis-ink/85',
};

export function SiteFooter() {
  return (
    <footer className="cr-on-dark w-full border-line bg-emphasis text-emphasis-ink dark:border-t">
      <div className="mx-auto w-full max-w-[1180px] px-5 pt-20 pb-10 sm:pt-24">
        <div className="grid grid-cols-1 gap-12 lg:grid-cols-[1.15fr_1fr]">
          {/* brand */}
          <div className="max-w-[42ch]">
            <div className="flex items-center gap-3">
              <Logomark className="h-[18px] w-auto text-emphasis-ink" />
              <span className="font-mono text-[15px] font-medium tracking-tight text-emphasis-ink">
                code-runner
              </span>
            </div>
            <p className="mt-4 text-[15px] leading-relaxed text-emphasis-ink/70">
              An open-source, self-hostable service for running untrusted code
              in a hardened sandbox, with live stdin and real-time output.
            </p>
            <div className="mt-6 flex flex-wrap items-center gap-3">
              <Link
                href="/docs/quickstart"
                className="inline-flex items-center gap-2 rounded-full bg-canvas px-4 py-2 text-[14px] font-medium text-ink transition-opacity duration-150 ease-snap hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:ring-offset-2 focus-visible:ring-offset-emphasis"
              >
                Get started
              </Link>
              <a
                href={REPO}
                className="inline-flex items-center gap-2 rounded-full border border-white/15 px-4 py-2 text-[14px] font-medium text-emphasis-ink transition-colors duration-150 ease-snap hover:border-white/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris"
              >
                GitHub
              </a>
            </div>
          </div>

          {/* docs map */}
          <nav
            aria-label="Footer"
            className="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-3"
          >
            {COLUMNS.map((col) => (
              <div key={col.heading}>
                <p className="font-mono text-[11px] uppercase tracking-[0.12em] text-emphasis-ink/40">
                  {col.heading}
                </p>
                <ul className="mt-4 flex flex-col gap-2.5">
                  {col.links.map((link) => (
                    <li key={link.href}>
                      <Link
                        href={link.href}
                        className="text-[14px] text-emphasis-ink/65 transition-colors duration-150 ease-snap hover:text-emphasis-ink focus-visible:outline-none focus-visible:text-emphasis-ink"
                      >
                        {link.label}
                      </Link>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </nav>
        </div>

        {/* the running machine — a continuous multiplexed worker log */}
        <div
          className="cr-marquee mt-16 h-[132px] rounded-card border border-white/10 bg-white/[0.02] px-4 py-3"
          aria-hidden
        >
          <div className="cr-marquee-track font-mono text-[12px] leading-[1.85]">
            {[...LOG, ...LOG].map((l, i) => (
              <div key={i} className="flex gap-2.5 whitespace-nowrap">
                <span className="select-none text-emphasis-ink/25">
                  {String(i % LOG.length).padStart(2, '0')}
                </span>
                <span className={l.tone ? TONE[l.tone] : 'text-emphasis-ink/45'}>
                  {l.text}
                </span>
              </div>
            ))}
          </div>
        </div>

        {/* bottom bar */}
        <div className="mt-10 flex flex-col gap-3 border-t border-white/[0.08] pt-6 sm:flex-row sm:items-center sm:justify-between">
          <p className="font-mono text-[12px] text-emphasis-ink/45">
            MIT licensed · built with Go, Hono &amp; Redis
          </p>
          <p className="font-mono text-[12px] tracking-[0.04em] text-emphasis-ink/45">
            precise · hardened · fast
          </p>
        </div>
      </div>
    </footer>
  );
}
