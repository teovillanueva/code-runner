import type { CSSProperties, ReactNode } from 'react';

/**
 * Run-outcome pill — the product's core vocabulary as an inline MDX token.
 *
 *   <StatusPill kind="running" />   ● running   (iris, pulsing)
 *   <StatusPill kind="ok" />        ● exit 0    (success)
 *   <StatusPill kind="warn" />      ● timeout   (warn)
 *   <StatusPill kind="err" />       ● killed    (error)
 *
 * Status is carried by a labelled dot, never colour alone (WCAG, colour-blind
 * safe). Geist Mono, 6px radius, faint same-hue wash. See DESIGN.md.
 */
type Kind = 'running' | 'ok' | 'warn' | 'err';

const DEFAULTS: Record<Kind, { color: string; label: string }> = {
  running: { color: 'var(--status-running)', label: 'running' },
  ok: { color: 'var(--status-ok)', label: 'exit 0' },
  warn: { color: 'var(--status-warn)', label: 'timeout' },
  err: { color: 'var(--status-err)', label: 'killed' },
};

export function StatusPill({
  kind = 'running',
  children,
}: {
  kind?: Kind;
  children?: ReactNode;
}) {
  const { color, label } = DEFAULTS[kind];

  return (
    <span
      className="not-prose inline-flex items-center gap-1.5 rounded-chip border px-2 py-0.5 align-middle font-mono text-[12px] leading-none"
      style={
        {
          color,
          borderColor: `color-mix(in oklab, ${color} 32%, var(--line))`,
          background: `color-mix(in oklab, ${color} 8%, transparent)`,
        } as CSSProperties
      }
    >
      {kind === 'running' ? (
        <span className="cr-dot" aria-hidden style={{ width: 6, height: 6 }} />
      ) : (
        <span
          aria-hidden
          style={{
            width: 6,
            height: 6,
            borderRadius: 9999,
            background: color,
            display: 'inline-block',
          }}
        />
      )}
      {children ?? label}
    </span>
  );
}
