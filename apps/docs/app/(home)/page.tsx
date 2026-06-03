import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { CopyButton } from './_components/copy-button';
import { HeroTerminal } from './_components/hero-terminal';
import { ArchitectureBand } from './_components/architecture-band';
import { LogoCloud, CaseStudy } from './_components/proof';
import { SiteFooter } from './_components/site-footer';

const QUICKSTART = `cp .env.example .env       # safe local defaults
make build-images          # build the language sandbox images
docker compose up          # redis + soketi + api + worker
make e2e                   # interactive "hello, world" round-trip`;

const SESSION_SDK = `import { CodeRunnerClient } from '@teovilla/code-runner-sdk-node';

const client = new CodeRunnerClient({ baseUrl, token });

const { jobId, channel } = await client.execute({
  language: 'python',
  files: [{ name: 'main.py', content: source }],
});

await client.start(jobId);              // worker boots the sandbox
// the browser is subscribed to \`channel\`, already streaming stdout
await client.sendStdin(jobId, 'ada\\n'); // written while it runs`;

const LANGUAGES = [
  { name: 'python', version: '3.12' },
  { name: 'rust', version: '1.83' },
  { name: 'r', version: '4.4' },
  { name: 'sqlite', version: '3' },
];

const HARDENING = [
  'network = none',
  'read-only rootfs',
  'cap-drop ALL',
  'seccomp: deny by default',
  'no-new-privileges',
  'mem · cpu · pids cgroups',
];

const CLOCKS = [
  { label: 'wall', detail: 'Total lifetime from start. The hard ceiling.', value: '30s' },
  { label: 'idle', detail: 'Time with no stdout and no stdin written.', value: '10s' },
  { label: 'cpu', detail: 'Accumulated scheduler time, ignores wall-clock.', value: '15s' },
];

export default function HomePage() {
  return (
    <main className="flex flex-1 flex-col bg-canvas text-ink">
      {/* ════════════ framed console: status → hero → languages ════════════ */}
      <div className="mx-auto w-full max-w-[1180px] border-x border-line">
        {/* status strip — sets the instrument-panel tone immediately */}
        <div className="flex items-center justify-between border-b border-line px-5 py-2.5 font-mono text-[12px] sm:px-8">
          <span className="flex items-center gap-2 text-ink-2">
            <span className="cr-dot" aria-hidden />
            internal code execution service
          </span>
          <span className="hidden items-center gap-2.5 text-ink-3 sm:flex">
            <span>MIT</span>
            <span aria-hidden>·</span>
            <span>self-hostable</span>
            <span aria-hidden>·</span>
            <span>docker-native</span>
          </span>
        </div>

        {/* hero */}
        <section className="relative overflow-hidden border-b border-line">
          <div className="pointer-events-none absolute inset-0 cr-grid" aria-hidden />
          <div className="relative grid grid-cols-1 items-center gap-12 px-5 pt-14 pb-16 sm:px-8 sm:pt-20 lg:grid-cols-[1.05fr_0.95fr] lg:gap-14 lg:pb-24">
            <div className="cr-reveal flex flex-col items-start">
              <h1 className="text-balance text-[clamp(2.15rem,5.4vw,3.5rem)] font-semibold leading-[1.04] tracking-[-0.028em]">
                Run untrusted code in a hardened, live sandbox.
              </h1>

              <p className="mt-5 max-w-[46ch] text-pretty text-[1.0625rem] leading-relaxed text-ink-2">
                code-runner is a Piston-style execution service: it isolates code
                in a locked-down container, streams stdout as it happens, and lets
                you write to stdin while the process is still running.
              </p>

              <div className="mt-8 flex flex-wrap items-center gap-3">
                <Link
                  href="/docs/quickstart"
                  className="group inline-flex items-center gap-2 rounded-full bg-ink px-5 py-2.5 text-[15px] font-medium text-canvas transition-opacity duration-150 ease-snap hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:ring-offset-2 focus-visible:ring-offset-canvas"
                >
                  Read the quickstart
                  <ArrowRight className="size-4 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
                </Link>
                <Link
                  href="/docs"
                  className="inline-flex items-center gap-2 rounded-full border border-iris px-5 py-2.5 text-[15px] font-medium text-iris transition-colors duration-150 ease-snap hover:bg-iris-wash focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris"
                >
                  Browse the docs
                </Link>
                <a
                  href="https://github.com/teovillanueva/code-runner"
                  className="inline-flex items-center gap-2 rounded-full px-3 py-2.5 text-[15px] font-medium text-ink-2 transition-colors duration-150 ease-snap hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris"
                >
                  GitHub
                </a>
              </div>
            </div>

            <div
              className="cr-reveal"
              style={{ '--cr-delay': '140ms' } as React.CSSProperties}
            >
              <HeroTerminal />
            </div>
          </div>
        </section>

        {/* language strip */}
        <section className="flex flex-col gap-4 px-5 py-6 sm:flex-row sm:items-center sm:justify-between sm:px-8">
          <p className="max-w-[34ch] text-sm text-ink-3">
            Four sandboxes ship today. Each language is a folder the worker
            discovers at boot.
          </p>
          <ul className="flex flex-wrap items-center gap-x-6 gap-y-3 font-mono text-sm">
            {LANGUAGES.map((l) => (
              <li key={l.name} className="flex items-baseline gap-1.5">
                <span className="text-ink">{l.name}</span>
                <span className="text-ink-3">{l.version}</span>
              </li>
            ))}
            <li className="text-iris">+ add your own</li>
          </ul>
        </section>

        {/* who already runs it — quiet production proof */}
        <LogoCloud />
      </div>

      {/* ═══════════ the machine opens up: request lifecycle (dark) ═══════════ */}
      <ArchitectureBand />

      {/* ════════════════ framed console resumes: capabilities ════════════════ */}
      <div className="mx-auto w-full max-w-[1180px] border-x border-line">
        {/* hardened */}
        <FeatureSplit
          title="Hardened by default, not by checklist."
          body="Every sandbox starts from a deny-by-default posture. There is no opt-in security to forget; the runner only relaxes what a language image actually needs. Swap the runtime to gVisor with a single env var when you want a second wall."
          docHref="/docs/concepts/sandbox-hardening"
          docLabel="The hardening model"
        >
          <div className="rounded-card border border-line bg-section p-5">
            <p className="font-mono text-[12px] text-ink-3">
              applied to every container
            </p>
            <ul className="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2">
              {HARDENING.map((flag) => (
                <li
                  key={flag}
                  className="flex items-center gap-2 font-mono text-[13px] text-ink-2"
                >
                  <span className="size-1.5 rounded-full bg-iris" aria-hidden />
                  {flag}
                </li>
              ))}
            </ul>
          </div>
        </FeatureSplit>

        {/* live stdin — shown as the real backend SDK call */}
        <FeatureSplit
          reverse
          title="A real session, not a batch job."
          body="Your backend pushes stdin into the still-running process through one SDK call. Output streams to the browser over soketi the moment a byte is written. The sandbox holds its pipes open for the length of the conversation."
          docHref="/docs/guides/interactive-stdin"
          docLabel="Interactive stdin guide"
        >
          <CodeCard title="server.ts" lang="ts">
            {SESSION_SDK}
          </CodeCard>
        </FeatureSplit>

        {/* three clocks — promoted to a full-width band, real manifest limits */}
        <section className="border-t border-line px-5 py-16 sm:px-8 lg:py-20">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
            <div>
              <h2 className="max-w-[22ch] text-balance text-[1.75rem] font-semibold leading-[1.13] tracking-[-0.02em]">
                Three clocks, and any one of them wins.
              </h2>
              <p className="mt-3 max-w-[52ch] text-pretty leading-relaxed text-ink-2">
                A session runs under wall time, idle time, and CPU time at once.
                The first to expire kills the sandbox unconditionally and frees
                its slot.
              </p>
            </div>
            <Link
              href="/docs/concepts/three-clocks"
              className="group inline-flex shrink-0 items-center gap-1.5 font-mono text-[13px] text-iris transition-colors duration-150 ease-snap hover:text-iris-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:rounded-chip"
            >
              How the clocks work
              <ArrowRight className="size-3.5 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
            </Link>
          </div>

          <div className="mt-10 grid grid-cols-1 gap-4 md:grid-cols-3">
            {CLOCKS.map((c) => (
              <div
                key={c.label}
                className="flex flex-col rounded-card border border-line bg-card p-5"
              >
                <div className="flex items-baseline justify-between">
                  <span className="font-mono text-[15px] text-ink">{c.label}</span>
                  <span className="rounded-chip bg-section px-2 py-0.5 font-mono text-[12px] text-ink-2">
                    {c.value}
                  </span>
                </div>
                <p className="mt-3 text-[14px] leading-relaxed text-ink-2">
                  {c.detail}
                </p>
              </div>
            ))}
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-card border border-line bg-section px-5 py-4 font-mono text-[13px]">
            <span className="inline-flex items-center gap-1.5 rounded-chip border border-line bg-canvas px-2 py-0.5 text-[11px]">
              <span className="size-1.5 rounded-full bg-status-err" aria-hidden />
              <span className="text-status-err">SIGKILL</span>
            </span>
            <span className="text-ink-2">
              first to expire → sandbox reaped → slot freed.
            </span>
            <span className="text-ink-3">
              Defaults shown are per-language, set in each manifest.
            </span>
          </div>
        </section>

        {/* add a language = a folder */}
        <section className="border-t border-line px-5 py-16 sm:px-8 lg:py-20">
          <h2 className="max-w-[24ch] text-balance text-[1.75rem] font-semibold leading-[1.15] tracking-[-0.02em]">
            Adding a language is adding a folder.
          </h2>
          <p className="mt-3 max-w-[60ch] text-ink-2">
            No core changes, no language hardcoded anywhere. Drop a manifest and a
            pre-built image into{' '}
            <code className="rounded-[5px] bg-section px-1.5 py-0.5 font-mono text-[0.85em] text-ink">
              languages/
            </code>
            ; the worker picks it up at boot.
          </p>

          <div className="mt-8 grid grid-cols-1 gap-4 lg:grid-cols-2">
            <CodeCard title="languages/go-1.23/manifest.json">
              {`{
  "language": "go",
  "version": "1.23",
  "aliases": ["golang"],
  "image": "executor/go:1.23",
  "entrypoint": "main.go",
  "run": ["go", "run", "main.go"],
  "interactive": true
}`}
            </CodeCard>
            <CodeCard title="languages/go-1.23/Dockerfile">
              {`FROM golang:1.23-alpine
# everything the run command needs,
# nothing it doesn't. read-only at
# runtime; the worker mounts code in.
WORKDIR /sandbox`}
            </CodeCard>
          </div>

          <Link
            href="/docs/guides/add-a-language"
            className="group mt-7 inline-flex items-center gap-1.5 font-mono text-[13px] text-iris transition-colors duration-150 ease-snap hover:text-iris-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:rounded-chip"
          >
            The add-a-language guide
            <ArrowRight className="size-3.5 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
          </Link>
        </section>

        {/* case study — edalef × Universidad de San Andrés exam platform */}
        <CaseStudy />

        {/* quickstart band — inset dark panel, the conversion moment */}
        <section className="border-t border-line px-5 py-16 sm:px-8 lg:py-20">
          <div className="cr-on-dark overflow-hidden rounded-card border-line bg-emphasis text-emphasis-ink dark:border">
            <div className="grid grid-cols-1 gap-8 p-7 sm:p-10 lg:grid-cols-[0.9fr_1.1fr] lg:items-center">
              <div>
                <p className="font-mono text-[11px] uppercase tracking-[0.14em] text-emphasis-ink/50">
                  from clone to round-trip
                </p>
                <h2 className="mt-3 text-balance text-[1.75rem] font-semibold leading-tight tracking-[-0.02em]">
                  Up and running in four commands.
                </h2>
                <p className="mt-3 max-w-[40ch] text-emphasis-ink/70">
                  A plain{' '}
                  <code className="font-mono text-emphasis-ink">
                    docker compose up
                  </code>{' '}
                  is a true no-op for telemetry; nothing leaves your machine until
                  you point it somewhere.
                </p>
                <Link
                  href="/docs/quickstart"
                  className="group mt-6 inline-flex items-center gap-2 rounded-full bg-canvas px-4 py-2 text-[14px] font-medium text-ink transition-opacity duration-150 ease-snap hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:ring-offset-2 focus-visible:ring-offset-emphasis"
                >
                  Full quickstart
                  <ArrowRight className="size-4 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
                </Link>
              </div>

              <div className="rounded-input border border-white/10 bg-white/[0.02]">
                <div className="flex items-center justify-between border-b border-white/10 px-4 py-2.5">
                  <span className="font-mono text-[12px] text-emphasis-ink/55">
                    bash
                  </span>
                  <CopyButton value={QUICKSTART} />
                </div>
                <pre className="overflow-x-auto px-4 py-4 font-mono text-[13px] leading-relaxed">
                  <code>
                    {QUICKSTART.split('\n').map((line) => {
                      const [cmd, comment] = line.split(/(\s{2,}#.*)/);
                      return (
                        <span key={line} className="block">
                          <span className="text-emphasis-ink">{cmd}</span>
                          {comment ? (
                            <span className="text-emphasis-ink/45">{comment}</span>
                          ) : null}
                        </span>
                      );
                    })}
                  </code>
                </pre>
              </div>
            </div>
          </div>
        </section>
      </div>

      {/* ════════════════════ the running machine: footer ════════════════════ */}
      <SiteFooter />
    </main>
  );
}

/* ────────────────────────────── pieces ────────────────────────────── */

function FeatureSplit({
  title,
  body,
  children,
  reverse,
  docHref,
  docLabel,
}: {
  title: string;
  body: string;
  children: React.ReactNode;
  reverse?: boolean;
  docHref?: string;
  docLabel?: string;
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-8 border-t border-line px-5 py-16 sm:px-8 lg:grid-cols-2 lg:gap-14 lg:py-20">
      <div className={reverse ? 'lg:order-2' : undefined}>
        <h2 className="max-w-[20ch] text-balance text-[1.75rem] font-semibold leading-[1.15] tracking-[-0.02em]">
          {title}
        </h2>
        <p className="mt-4 max-w-[48ch] text-pretty leading-relaxed text-ink-2">
          {body}
        </p>
        {docHref && docLabel ? (
          <Link
            href={docHref}
            className="group mt-6 inline-flex items-center gap-1.5 font-mono text-[13px] text-iris transition-colors duration-150 ease-snap hover:text-iris-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris focus-visible:rounded-chip"
          >
            {docLabel}
            <ArrowRight className="size-3.5 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
          </Link>
        ) : null}
      </div>
      <div className={reverse ? 'lg:order-1' : undefined}>{children}</div>
    </div>
  );
}

function CodeCard({
  title,
  children,
  lang,
}: {
  title: string;
  children: string;
  lang?: string;
}) {
  return (
    <div className="overflow-hidden rounded-card border border-line bg-section">
      <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
        <span className="font-mono text-[12px] text-ink-3">{title}</span>
        {lang ? (
          <span className="font-mono text-[11px] text-ink-3/80">{lang}</span>
        ) : null}
      </div>
      <pre className="overflow-x-auto px-4 py-4 font-mono text-[13px] leading-relaxed text-ink-2">
        <code>{children}</code>
      </pre>
    </div>
  );
}
