import Link from 'next/link';
import { ArrowRight, Lock, Shield } from 'lucide-react';

/**
 * The trust payload, drawn as one honest line. A request lifecycle is not a
 * tangle of boxes and arrows: it is a single locked path read top to bottom —
 * the trusted control plane descends into the trust boundary (the one hero
 * element), the sandbox runs untrusted code beyond it, and output is the only
 * thing that crosses back, through an output-only relay. stdin is the single
 * exception, called out as a footnote. Iris is rationed to the boundary and
 * the sandbox; the spine and trusted stations stay achromatic.
 *
 * Full-bleed inverted band — the first dark "machine" moment. `.cr-on-dark`
 * remaps the accent tokens for the emphasis surface; `.cr-spine` is a
 * server-rendered vertical signal bus that pulses a request down the path.
 */

type Marker = 'external' | 'trusted' | 'sandbox' | 'exit';
type Station = { marker: Marker; name: string; sub: string; hop?: string };

// the trusted control plane, in order. `hop` is the wire move to the next stop.
const TRUSTED: Station[] = [
  { marker: 'external', name: 'your backend', sub: 'any stack · bearer token', hop: 'POST /run' },
  { marker: 'trusted', name: 'api', sub: 'Hono · validates, enqueues', hop: 'enqueue' },
  { marker: 'trusted', name: 'redis', sub: 'job queue + stdin bus', hop: 'BRPOP · claim slot' },
  { marker: 'trusted', name: 'worker', sub: 'Go · attaches stdio', hop: 'create · attach' },
];

const SANDBOX_FLAGS = ['network=none', 'ro-rootfs', 'cap-drop ALL', 'seccomp'];

export function ArchitectureBand() {
  return (
    <section className="cr-on-dark w-full border-line bg-emphasis text-emphasis-ink dark:border-y">
      <div className="mx-auto w-full max-w-[1180px] px-5 py-20 sm:py-24">
        <div className="flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <p className="font-mono text-[11px] uppercase tracking-[0.14em] text-emphasis-ink/45">
              Request lifecycle
            </p>
            <h2 className="mt-3 max-w-[20ch] text-balance text-[1.75rem] font-semibold leading-[1.12] tracking-[-0.02em]">
              Every execution takes the same locked path.
            </h2>
          </div>
          <Link
            href="/docs/concepts/architecture"
            className="group inline-flex items-center gap-1.5 self-start font-mono text-[13px] text-iris transition-colors duration-150 ease-snap hover:text-iris-hover focus-visible:rounded-chip focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris sm:self-auto"
          >
            Read the architecture
            <ArrowRight className="size-3.5 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
          </Link>
        </div>

        {/* asymmetric split: the linear trace on the left, the trust model it
            proves on the right — vertically centered so the band reads as one
            composed plate rather than a diagram floating in a void */}
        <div className="mt-11 grid grid-cols-1 gap-x-12 gap-y-9 lg:grid-cols-[minmax(0,1fr)_minmax(0,0.78fr)] lg:items-center">
          {/* one linear trace: trusted plane → wall → sandbox → output relay */}
          <div className="overflow-hidden rounded-card border border-white/[0.1] bg-white/[0.015]">
            {/* trusted control plane */}
            <ol className="px-5 pt-6 pb-2 sm:px-7">
              {TRUSTED.map((stage, i) => (
                <StationRow key={stage.name} stage={stage} delay={i} />
              ))}
            </ol>

            {/* the hero: a hardened seam the path is forced through */}
            <Wall />

            {/* beyond the wall — the one iris-tinted zone */}
            <div className="bg-iris/[0.04] px-5 pt-5 pb-6 sm:px-7">
              <ol>
              <StationRow
                stage={{
                  marker: 'sandbox',
                  name: 'sandbox',
                  sub: 'runs untrusted code',
                  hop: 'output-only',
                }}
                delay={4}
                last
              >
                <ul className="mt-2.5 flex flex-wrap gap-1.5">
                  {SANDBOX_FLAGS.map((flag) => (
                    <li
                      key={flag}
                      className="rounded-[5px] border border-iris/25 bg-iris/[0.07] px-1.5 py-0.5 font-mono text-[10px] text-iris/90"
                    >
                      {flag}
                    </li>
                  ))}
                </ul>
              </StationRow>
              <StationRow
                stage={{
                  marker: 'exit',
                  name: 'soketi → browser',
                  sub: 'live stream, output-only',
                }}
                delay={5}
                last
                terminal
              />
              </ol>
            </div>
          </div>

          {/* the trust model the trace proves, reading down the same axis */}
          <div className="flex flex-col gap-6">
            <p className="max-w-[42ch] text-[15px] leading-relaxed text-emphasis-ink/70">
              Everything above the wall is trusted and authenticated. Beyond it,
              the sandbox runs with{' '}
              <span className="text-emphasis-ink">no network</span> and a
              read-only root. Output leaves through soketi, which is{' '}
              <span className="text-emphasis-ink">output-only</span> toward the
              client; nothing trusted ever enters that way.
            </p>

            {/* the single trust exception, as a note rather than a third flow */}
            <p className="flex max-w-[40ch] items-start gap-2.5 border-t border-white/[0.08] pt-5 font-mono text-[12px] leading-relaxed text-emphasis-ink/55">
              <Lock className="mt-0.5 size-3.5 shrink-0 text-iris/70" aria-hidden />
              <span>
                <span className="text-emphasis-ink/85">stdin</span> is the only
                thing that re-enters mid-run, and only back through the trusted
                worker, never through soketi.
              </span>
            </p>
          </div>
        </div>
      </div>
    </section>
  );
}

const MARKER: Record<Marker, string> = {
  external:
    'size-[9px] rounded-full border border-dashed border-emphasis-ink/45 bg-transparent',
  trusted: 'size-[9px] rounded-full border border-emphasis-ink/40 bg-emphasis-ink/10',
  sandbox: 'size-[11px] rounded-[3px] border border-iris bg-iris/30',
  exit: 'size-[9px] rounded-full border border-iris/55 bg-iris/15',
};

function StationRow({
  stage,
  delay,
  last,
  terminal,
  children,
}: {
  stage: Station;
  delay: number;
  last?: boolean;
  terminal?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <li className="flex gap-4">
      {/* spine column: continuous vertical trace + the station marker on it */}
      <div className="relative flex w-3 flex-none justify-center">
        {!terminal && (
          <span
            className="cr-spine"
            style={{ '--cr-seg-delay': `${delay * 260}ms` } as React.CSSProperties}
            aria-hidden
          />
        )}
        <span
          className={`absolute top-[6px] ${MARKER[stage.marker]}`}
          aria-hidden
        />
      </div>

      {/* station body */}
      <div className={`min-w-0 flex-1 ${last ? 'pb-0' : 'pb-1'}`}>
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
          <span className="inline-flex items-center gap-2 font-mono text-[14px] text-emphasis-ink">
            {stage.name}
            {stage.marker === 'sandbox' && (
              <span className="cr-dot" aria-hidden />
            )}
          </span>
          <span className="font-mono text-[11px] text-emphasis-ink/50">
            {stage.sub}
          </span>
        </div>

        {children}

        {/* the hop label rides the gap leading to the next station */}
        {stage.hop && (
          <p className="mt-2 mb-3 font-mono text-[10px] uppercase tracking-[0.1em] text-emphasis-ink/40">
            {stage.hop}
          </p>
        )}
      </div>
    </li>
  );
}

function Wall() {
  return (
    <div className="relative flex items-center gap-3 px-5 py-1 sm:px-7">
      <span className="h-px flex-1 bg-[linear-gradient(to_right,transparent,color-mix(in_oklab,var(--iris)_55%,transparent))]" />
      <span className="inline-flex items-center gap-1.5 rounded-chip border border-iris/45 bg-iris/[0.1] px-2.5 py-1 font-mono text-[10px] uppercase tracking-[0.16em] text-iris">
        <Shield className="size-3" aria-hidden />
        trust boundary
      </span>
      <span className="h-px flex-1 bg-[linear-gradient(to_left,transparent,color-mix(in_oklab,var(--iris)_55%,transparent))]" />
    </div>
  );
}
