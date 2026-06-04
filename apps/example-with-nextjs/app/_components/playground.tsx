"use client";

import dynamic from "next/dynamic";
import { useEffect, useState } from "react";
import { CodeRunnerProvider } from "@teovilla/code-runner-react";
import type { SoketiPublicConfig } from "@/app/lib/public-config";
import { LANGUAGES, type LanguagePreset } from "./languages";
import { JobView } from "./job-view";

// Monaco touches `window`; load it client-only.
const Editor = dynamic(
  () => import("@monaco-editor/react").then((m) => m.Editor),
  { ssr: false, loading: () => <div className="editor-wrap" /> },
);

interface ActiveJob {
  jobId: string;
  channel: string;
}

export function Playground({ soketi }: { soketi: SoketiPublicConfig }) {
  const [preset, setPreset] = useState<LanguagePreset>(LANGUAGES[0]);
  const [code, setCode] = useState<string>(LANGUAGES[0].snippet);
  const [job, setJob] = useState<ActiveJob | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // pusher-js (inside CodeRunnerProvider) touches browser globals, so only mount
  // the provider after hydration. A job can only exist post-mount (it needs a
  // click), so JobView never renders without the provider.
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  function selectPreset(id: string) {
    const next = LANGUAGES.find((l) => l.id === id) ?? LANGUAGES[0];
    setPreset(next);
    setCode(next.snippet);
    setJob(null);
    setError(null);
  }

  async function run() {
    setPending(true);
    setError(null);
    setJob(null);
    try {
      const res = await fetch("/api/execute", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          language: preset.language,
          version: preset.version,
          files: [{ name: preset.file, content: code }],
          collectOutput: true,
          // Generous interactive limits for a human-paced playground: the
          // manifest defaults (python idleMs 10s / wallTimeMs 30s) are tuned for
          // batch runs and idle-kill a prompt before you can read+type. The
          // worker still resets the idle clock on every stdin chunk; these just
          // widen the windows. Tune per your own UX/slot-cost tradeoff.
          limits: { idleMs: 60_000, wallTimeMs: 120_000 },
        }),
      });
      const data = (await res.json()) as {
        jobId?: string;
        channel?: string;
        error?: string;
      };
      if (!res.ok || !data.jobId || !data.channel) {
        throw new Error(data.error ?? `execute failed (HTTP ${res.status})`);
      }
      setJob({ jobId: data.jobId, channel: data.channel });
    } catch (e) {
      setError(e instanceof Error ? e.message : "execute failed");
    } finally {
      setPending(false);
    }
  }

  const shell = (
    <div className="shell">
        <header className="header">
          <div>
            <h1>code-runner · Next.js playground</h1>
            <div className="sub">
              Monaco editor wired to{" "}
              <code>@teovilla/code-runner-sdk-node</code> (server) +{" "}
              <code>@teovilla/code-runner-react</code> (live output).
            </div>
          </div>
          <div className="meta">
            soketi {soketi.host}:{soketi.port}
          </div>
        </header>

        <div className="toolbar">
          <select
            value={preset.id}
            onChange={(e) => selectPreset(e.target.value)}
            aria-label="Language"
          >
            {LANGUAGES.map((l) => (
              <option key={l.id} value={l.id}>
                {l.label}
              </option>
            ))}
          </select>
          <button onClick={run} disabled={pending}>
            {pending ? "Starting…" : "▶ Run"}
          </button>
        </div>

        <div className="grid">
          <div className="panel">
            <div className="panel-head">
              <span>{preset.file}</span>
              <span className="meta">{preset.language}</span>
            </div>
            <div className="editor-wrap">
              <Editor
                height="100%"
                theme="vs-dark"
                language={preset.monaco}
                value={code}
                onChange={(v) => setCode(v ?? "")}
                options={{
                  minimap: { enabled: false },
                  fontSize: 13,
                  scrollBeyondLastLine: false,
                  automaticLayout: true,
                }}
              />
            </div>
          </div>

          <div>
            {job ? (
              <JobView key={job.jobId} jobId={job.jobId} />
            ) : (
              <div className="panel">
                <div className="panel-head">
                  <span>Output</span>
                  <span className="badge idle">idle</span>
                </div>
                <pre className="console">
                  {`Press “Run” to execute the code.\nLive stdout/stderr stream over soketi via useCodeRunnerJob.`}
                </pre>
              </div>
            )}
          </div>
        </div>

        {error ? <div className="error-note">{error}</div> : null}
    </div>
  );

  if (!mounted) return shell;

  return (
    <CodeRunnerProvider
      appKey={soketi.appKey}
      host={soketi.host}
      port={soketi.port}
      useTLS={soketi.useTLS}
      authEndpoint="/api/channel-auth"
    >
      {shell}
    </CodeRunnerProvider>
  );
}
