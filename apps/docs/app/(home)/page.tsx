import Link from 'next/link';
import {
  ArrowRight,
  Boxes,
  GaugeCircle,
  KeyRound,
  Layers,
  Radio,
  ShieldCheck,
  TerminalSquare,
} from 'lucide-react';

const features = [
  {
    icon: ShieldCheck,
    title: 'Hardened sandboxes',
    body: 'Untrusted code runs with network=none, a read-only rootfs, dropped capabilities, a deny-by-default seccomp profile, and cgroup memory/CPU/PID limits. gVisor is a one-env-var swap.',
  },
  {
    icon: Radio,
    title: 'Live interactive stdin',
    body: 'Stream output in real time over soketi (Pusher-compatible) and write to the running process stdin mid-execution. True interactive sessions, not batch-only.',
  },
  {
    icon: GaugeCircle,
    title: 'Three independent clocks',
    body: 'Every sandbox is bounded by wall time, idle time, and CPU time. Any clock expiry kills it unconditionally — no leaked containers, no leaked slots.',
  },
  {
    icon: Layers,
    title: 'Polyglot by design',
    body: 'A Hono/TypeScript gateway, a Go worker, and a JSON-Schema wire contract that generates TS types, Zod validators, and Go structs from one source.',
  },
  {
    icon: Boxes,
    title: 'Add a language = a folder',
    body: 'Each language is a manifest.json + Dockerfile. The worker auto-discovers it at boot. Zero changes to Go or the API.',
  },
  {
    icon: KeyRound,
    title: 'Self-hostable & MIT',
    body: 'docker compose up for local dev, a Fly.io reference deploy, and autoscale-by-queue-depth for production. Bring your own OpenTelemetry.',
  },
];

export default function HomePage() {
  return (
    <main className="flex flex-1 flex-col">
      <section className="mx-auto flex w-full max-w-5xl flex-col items-center px-4 py-20 text-center sm:py-28">
        <span className="mb-6 inline-flex items-center gap-2 rounded-full border px-4 py-1.5 text-sm text-fd-muted-foreground">
          <TerminalSquare className="size-4" />
          Open-source remote code execution
        </span>
        <h1 className="max-w-3xl text-balance text-4xl font-bold tracking-tight sm:text-6xl">
          Run untrusted code in a hardened, interactive sandbox.
        </h1>
        <p className="mt-6 max-w-2xl text-balance text-lg text-fd-muted-foreground">
          code-runner is a Piston-style execution service with live stdin and
          real-time output streaming. Self-hostable, horizontally scalable, and
          trivially extensible — add a language by adding a folder.
        </p>
        <div className="mt-10 flex flex-wrap items-center justify-center gap-3">
          <Link
            href="/docs/quickstart"
            className="inline-flex items-center gap-2 rounded-lg bg-fd-primary px-5 py-2.5 font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
          >
            Quickstart <ArrowRight className="size-4" />
          </Link>
          <Link
            href="/docs"
            className="inline-flex items-center gap-2 rounded-lg border px-5 py-2.5 font-medium transition-colors hover:bg-fd-accent"
          >
            Read the docs
          </Link>
          <a
            href="https://github.com/teovillanueva/code-runner"
            className="inline-flex items-center gap-2 rounded-lg border px-5 py-2.5 font-medium transition-colors hover:bg-fd-accent"
          >
            GitHub
          </a>
        </div>
      </section>

      <section className="mx-auto grid w-full max-w-5xl grid-cols-1 gap-px overflow-hidden rounded-xl border bg-fd-border sm:grid-cols-2 lg:grid-cols-3">
        {features.map((f) => (
          <div key={f.title} className="flex flex-col gap-3 bg-fd-background p-6">
            <f.icon className="size-6 text-fd-primary" />
            <h3 className="font-semibold">{f.title}</h3>
            <p className="text-sm text-fd-muted-foreground">{f.body}</p>
          </div>
        ))}
      </section>

      <section className="mx-auto w-full max-w-5xl px-4 py-20">
        <div className="rounded-xl border bg-fd-card p-8">
          <h2 className="text-xl font-semibold">Up and running in four commands</h2>
          <pre className="mt-4 overflow-x-auto rounded-lg bg-fd-secondary p-4 text-sm">
            <code>{`cp .env.example .env       # safe local defaults
make build-images          # build the language sandbox images
docker compose up          # redis + soketi + api + worker
make e2e                   # interactive "hello World" round-trip`}</code>
          </pre>
          <Link
            href="/docs/quickstart"
            className="mt-6 inline-flex items-center gap-2 text-sm font-medium text-fd-primary hover:underline"
          >
            Full quickstart <ArrowRight className="size-4" />
          </Link>
        </div>
      </section>
    </main>
  );
}
