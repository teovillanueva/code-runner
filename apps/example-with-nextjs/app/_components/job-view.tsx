"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { useCodeRunnerJob } from "@teovilla/code-runner-react";
import type { JobState, RunResult } from "@teovilla/code-runner-contract";

// Live view of a single job. `useCodeRunnerJob` subscribes to the
// private-run-<jobId> soketi channel (output-only). Actions delegate to backend
// route handlers because the browser has no bearer token.
export function JobView({ jobId }: { jobId: string }) {
  const [stdin, setStdin] = useState("");
  // The start-handshake: only POST /start AFTER soketi confirms our
  // subscription, so we don't miss output emitted before we're listening (jobs
  // can finish in single-digit milliseconds). Guard against re-fires.
  const started = useRef(false);
  // The authoritative persisted result, pulled once the job is done. This is
  // where the Piston-style `compile` block lives (build logs kept separate from
  // the run stdout/stderr).
  const [output, setOutput] = useState<RunResult | null>(null);

  const job = useCodeRunnerJob({
    jobId,
    onSubscribed: async () => {
      if (started.current) return;
      started.current = true;
      await fetch(`/api/jobs/${jobId}/start`, { method: "POST" });
    },
    // Late-join reconciliation: pull the authoritative status once subscribed so
    // a job that already advanced past "queued" doesn't sit stale. The hook only
    // adopts it if it's ahead of the live state.
    onResolveStatus: async () => {
      const res = await fetch(`/api/jobs/${jobId}/status`);
      if (!res.ok) return null;
      const data = (await res.json()) as { state?: JobState };
      return data.state ?? null;
    },
    onStdin: async (chunk) => {
      await fetch(`/api/jobs/${jobId}/stdin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ chunk }),
      });
    },
    onKill: async () => {
      await fetch(`/api/jobs/${jobId}/kill`, { method: "POST" });
    },
  });

  // Pull the persisted RunResult once the job finishes — it carries the
  // separate compile block (GET /api/jobs/:id/output → sdk-node getOutput).
  useEffect(() => {
    if (job.status !== "done") return;
    let cancelled = false;
    void fetch(`/api/jobs/${jobId}/output`)
      .then((r) => (r.ok ? r.json() : null))
      .then((d: RunResult | { error: string } | null) => {
        if (!cancelled && d && !("error" in d)) setOutput(d);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [job.status, jobId]);

  async function submitStdin(e: FormEvent) {
    e.preventDefault();
    if (!stdin) return;
    const line = stdin.endsWith("\n") ? stdin : `${stdin}\n`;
    setStdin("");
    await job.sendStdin(line);
  }

  // Single coherent badge label. `stage` (queued/compiling/running) is more
  // granular than `status` while the job is live, so prefer it; collapse to
  // "done" at the end. Avoids redundant "queued · queued" / "running · running"
  // and the contradictory "done · running" (stage holds its last value).
  const label = job.status === "done" ? "done" : (job.stage ?? job.status);
  const badgeClass =
    job.status === "done"
      ? "badge done"
      : job.status === "running"
        ? "badge"
        : "badge idle";

  // Build log: live stream while compiling, persisted compile.output once done.
  const buildLog = job.compileOutput || output?.compile?.output || "";
  const compileMeta = output?.compile ?? null;
  const showBuild = buildLog.length > 0 || job.stage === "compiling";

  // Artifacts: prefer the authoritative persisted list once pulled, else the
  // live soketi `artifact` events. Each URL is a presigned GET — no bearer.
  const artifacts = output?.artifacts?.length ? output.artifacts : job.artifacts;

  return (
    <div className="panel">
      <div className="panel-head">
        <span>Output</span>
        <span className={badgeClass}>{label}</span>
      </div>

      {showBuild ? (
        <>
          <div className="panel-head" style={{ borderTop: 0 }}>
            <span>build{job.stage === "compiling" ? " · compiling…" : ""}</span>
            {compileMeta ? (
              <span className="meta">
                exit {String(compileMeta.exitCode)} · {compileMeta.durationMs}ms
              </span>
            ) : null}
          </div>
          <pre
            className={`console${compileMeta?.exitCode ? " stderr" : ""}`}
            style={{ height: 140 }}
          >
            {buildLog || " "}
          </pre>
        </>
      ) : null}

      <div className="panel-head" style={{ borderTop: 0 }}>
        <span>stdout</span>
        <span className="meta">{job.stdout.length} bytes</span>
      </div>
      <pre className="console">{job.stdout || " "}</pre>

      {job.stderr ? (
        <>
          <div className="panel-head" style={{ borderTop: "1px solid var(--border)" }}>
            <span>stderr</span>
          </div>
          <pre className="console stderr">{job.stderr}</pre>
        </>
      ) : null}

      {job.result ? (
        <div className="panel-head" style={{ borderTop: "1px solid var(--border)" }}>
          <span className="meta">
            exit {String(job.result.exitCode)}
            {job.result.signal ? ` · signal ${job.result.signal}` : ""}
            {job.result.timedOut ? " · timed out" : ""} · {job.result.durationMs}ms
          </span>
        </div>
      ) : null}

      {artifacts.length > 0 ? (
        <>
          <div className="panel-head" style={{ borderTop: "1px solid var(--border)" }}>
            <span>artifacts</span>
            <span className="meta">{artifacts.length}</span>
          </div>
          <ul className="artifacts">
            {artifacts.map((a) => (
              <li
                key={a.name}
                style={{ display: "flex", gap: 12, alignItems: "center" }}
              >
                {a.mimeType.startsWith("image/") ? (
                  <a href={a.url} target="_blank" rel="noreferrer">
                    {/* Presigned URL → fetched directly by the browser, no bearer. */}
                    {/* eslint-disable-next-line @next/next/no-img-element */}
                    <img
                      src={a.url}
                      alt={a.name}
                      style={{
                        maxWidth: 180,
                        maxHeight: 130,
                        borderRadius: 6,
                        border: "1px solid var(--border)",
                        display: "block",
                      }}
                    />
                  </a>
                ) : null}
                <span>
                  <a href={a.url} target="_blank" rel="noreferrer">
                    {a.name}
                  </a>{" "}
                  <span className="meta">
                    ({a.mimeType}, {a.bytes} bytes)
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </>
      ) : null}

      <div style={{ padding: "12px 14px" }}>
        <form className="stdin-row" onSubmit={submitStdin}>
          <input
            value={stdin}
            onChange={(e) => setStdin(e.target.value)}
            placeholder="type stdin and press Enter…"
            disabled={job.status === "done"}
            aria-label="stdin"
          />
          <button type="submit" className="ghost" disabled={job.status === "done"}>
            Send
          </button>
          <button
            type="button"
            className="danger"
            onClick={() => job.kill()}
            disabled={job.status === "done"}
          >
            Kill
          </button>
        </form>
      </div>
    </div>
  );
}
