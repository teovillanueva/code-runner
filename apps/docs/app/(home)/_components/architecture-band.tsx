import { Fragment } from 'react';
import Link from 'next/link';
import { ArrowRight, ChevronDown, ChevronRight, Lock, Shield } from 'lucide-react';

/**
 * The trust payload: the exact path one execution takes through the system,
 * drawn as a single hardened schematic rather than boxes-and-arrows. The
 * trust boundary is a real wall — the control plane flows left → right into
 * it; the sandbox is quarantined on the far side carrying its hardening; the
 * two return paths (output / stdin) fold into the same readout and mark where
 * they cross the wall. Full-bleed inverted band, the first dark "machine"
 * moment. `.cr-on-dark` remaps the accent/status tokens for the emphasis
 * surface; the connector rail is a server-rendered CSS signal bus.
 */

type Stage = { kicker: string; name: string; sub: string; external?: boolean };

const TRUSTED: Stage[] = [
  { kicker: 'external', name: 'your backend', sub: 'any stack · bearer token', external: true },
  { kicker: 'gateway', name: 'api', sub: 'Hono · validates, enqueues' },
  { kicker: 'queue', name: 'redis', sub: 'job queue + stdin bus' },
  { kicker: 'orchestrator', name: 'worker', sub: 'Go · claims a slot' },
];

// one hop label per gap, including the final crossing into the wall.
const HOPS = ['POST /run', 'enqueue', 'BRPOP · slot', 'create · attach'];

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

        {/* one framed schematic readout: control plane in → wall → sandbox,
            then the two return paths fold in beneath the same frame */}
        <div className="mt-11 overflow-hidden rounded-card border border-white/[0.1] bg-white/[0.015]">
          {/* zone strip — names the trust split before the eye parses the nodes */}
          <div className="flex items-center justify-between border-b border-white/[0.07] px-4 py-2.5 font-mono text-[10px] uppercase tracking-[0.14em] sm:px-6">
            <span className="text-emphasis-ink/40">control plane · trusted</span>
            <span className="inline-flex items-center gap-1.5 text-iris">
              <Lock className="size-3" aria-hidden />
              untrusted sandbox
            </span>
          </div>

          {/* control plane: trusted nodes → wall → sandbox */}
          <div className="flex flex-col gap-3 p-4 sm:p-6 lg:flex-row lg:items-stretch lg:gap-0">
            {TRUSTED.map((stage, i) => (
              <Fragment key={stage.name}>
                <Node stage={stage} />
                <Connector label={HOPS[i]} delay={i} />
              </Fragment>
            ))}
            <Wall />
            <SandboxNode />
          </div>

          {/* return paths — same frame, hairline-separated, crossing marked */}
          <div className="border-t border-white/[0.07]">
            <ReturnLane
              label="output"
              nodes={['sandbox', 'soketi', 'browser']}
              lockIndex={1}
              crossingAfter={0}
              note="soketi is output-only; it never accepts trusted input"
            />
            <div className="border-t border-white/[0.05]" />
            <ReturnLane
              label="stdin"
              nodes={['your backend', 'redis', 'worker', 'sandbox']}
              crossingAfter={2}
              note="written mid-run; re-enters only through the trusted plane"
            />
          </div>
        </div>

        <p className="mt-7 max-w-[68ch] text-[15px] leading-relaxed text-emphasis-ink/70">
          Everything left of the wall is trusted and authenticated. Beyond it,
          the sandbox runs untrusted code with{' '}
          <span className="text-emphasis-ink">no network</span> and a read-only
          root. Output leaves through soketi, which is{' '}
          <span className="text-emphasis-ink">output-only</span> toward the
          client; nothing trusted ever enters that way.
        </p>
      </div>
    </section>
  );
}

function Node({ stage }: { stage: Stage }) {
  return (
    <div
      className={`flex flex-col justify-center rounded-input px-3.5 py-3 lg:min-h-[82px] lg:min-w-0 lg:flex-[2] ${
        stage.external
          ? 'border border-dashed border-white/[0.2] bg-transparent'
          : 'border border-white/[0.1] bg-white/[0.03]'
      }`}
    >
      <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-emphasis-ink/45">
        {stage.kicker}
      </p>
      <p className="mt-1 font-mono text-[14px] text-emphasis-ink">{stage.name}</p>
      <p className="mt-0.5 font-mono text-[11px] leading-snug text-emphasis-ink/55">
        {stage.sub}
      </p>
    </div>
  );
}

function SandboxNode() {
  return (
    <div className="flex flex-col justify-center rounded-input border border-iris/55 bg-iris/[0.08] px-3.5 py-3 lg:min-h-[82px] lg:min-w-0 lg:flex-[2.9]">
      <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-iris">
        untrusted
      </p>
      <p className="mt-1 font-mono text-[14px] text-emphasis-ink">sandbox</p>
      <ul className="mt-2 flex flex-wrap gap-1">
        {SANDBOX_FLAGS.map((flag) => (
          <li
            key={flag}
            className="rounded-[5px] border border-iris/25 bg-iris/[0.06] px-1.5 py-0.5 font-mono text-[10px] text-iris/90"
          >
            {flag}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Connector({ label, delay }: { label: string; delay: number }) {
  return (
    <div className="flex items-center justify-center lg:min-w-0 lg:flex-1 lg:flex-col lg:gap-2 lg:px-1">
      {/* mobile: label + downward chevron between stacked nodes */}
      <div className="flex items-center gap-2 lg:hidden">
        <span className="font-mono text-[10px] uppercase tracking-[0.08em] text-emphasis-ink/40">
          {label}
        </span>
        <ChevronDown className="size-4 text-iris" aria-hidden />
      </div>

      {/* desktop: label rides the bus; the rail sweeps a signal in sequence */}
      <span className="hidden text-center font-mono text-[9.5px] uppercase leading-tight tracking-[0.07em] text-emphasis-ink/40 lg:block">
        {label}
      </span>
      <div className="hidden w-full items-center lg:flex">
        <div
          className="cr-rail flex-1"
          style={{ '--cr-seg-delay': `${delay * 300}ms` } as React.CSSProperties}
        />
        <ChevronRight className="size-3.5 shrink-0 text-iris/70" aria-hidden />
      </div>
    </div>
  );
}

function Wall() {
  return (
    <div className="flex shrink-0 items-center justify-center py-1 lg:w-[46px] lg:py-0">
      {/* mobile: a horizontal hardened divider */}
      <div className="flex w-full items-center gap-2.5 lg:hidden">
        <span className="h-px flex-1 border-t border-dashed border-iris/40" />
        <span className="inline-flex items-center gap-1.5 font-mono text-[9.5px] uppercase tracking-[0.14em] text-iris/80">
          <Shield className="size-3" aria-hidden />
          trust boundary
        </span>
        <span className="h-px flex-1 border-t border-dashed border-iris/40" />
      </div>

      {/* desktop: a vertical hardened wall with a spine label cut into it */}
      <div className="relative hidden h-full w-full items-center justify-center lg:flex">
        <span className="absolute inset-y-1 left-1/2 -translate-x-1/2 border-l border-dashed border-iris/45" />
        <span className="relative bg-emphasis px-1 py-2 font-mono text-[9px] uppercase tracking-[0.16em] text-iris/85 [writing-mode:vertical-rl] rotate-180">
          trust boundary
        </span>
      </div>
    </div>
  );
}

function ReturnLane({
  label,
  nodes,
  note,
  crossingAfter,
  lockIndex,
}: {
  label: string;
  nodes: string[];
  note: string;
  crossingAfter: number;
  lockIndex?: number;
}) {
  return (
    <div className="flex flex-col gap-2.5 px-4 py-4 sm:flex-row sm:items-center sm:gap-5 sm:px-6">
      <span className="inline-flex w-fit shrink-0 items-center rounded-chip bg-iris-wash px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em] text-iris">
        {label}
      </span>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 font-mono text-[12.5px]">
        {nodes.map((node, i) => (
          <span key={node} className="flex items-center gap-2">
            <span className="inline-flex items-center gap-1.5 text-emphasis-ink/85">
              {node}
              {i === lockIndex ? (
                <Lock className="size-3 text-iris/70" aria-hidden />
              ) : null}
            </span>
            {i < nodes.length - 1 ? (
              i === crossingAfter ? (
                <span className="inline-flex items-center gap-1.5" aria-hidden>
                  <span className="h-3.5 w-px border-l border-dashed border-iris/55" />
                  <ChevronRight className="size-3.5 text-iris" />
                </span>
              ) : (
                <ChevronRight className="size-3.5 text-iris" aria-hidden />
              )
            ) : null}
          </span>
        ))}
      </div>
      <p className="font-mono text-[11px] leading-snug text-emphasis-ink/55 sm:ml-auto sm:max-w-[34ch] sm:text-right">
        {note}
      </p>
    </div>
  );
}
