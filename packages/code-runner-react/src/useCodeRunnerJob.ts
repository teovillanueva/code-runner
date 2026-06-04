// useCodeRunnerJob — subscribe to a job's private-run-<jobId> soketi channel and
// reassemble ordered stdout/stderr from seq-numbered chunks.
//
// Actions (sendStdin/kill) are NOT performed here — the browser has no bearer
// token. They delegate to caller-provided callbacks that hit the user's backend
// (which uses sdk-node).

import type {
  Artifact,
  JobState,
  OutputChunkEvent,
  ResultEvent,
  StageEvent,
  StagePhase,
} from "@teovilla/code-runner-contract";
import { useEffect, useMemo, useRef, useState } from "react";
import { usePusher } from "./provider.tsx";

// soketi event names on the private-run-<jobId> channel (mirrors
// @teovilla/code-runner-contract `events`).
const EVENTS = {
  stage: "stage",
  stdout: "stdout",
  stderr: "stderr",
  result: "result",
  artifact: "artifact",
  compileOutput: "compile_output",
} as const;

// "idle"   — no job/channel to track yet.
// "queued" — accepted and waiting for a worker to claim it (the window between
//            /execute and the first run/compile stage; includes the wire
//            `queued` stage). Without this the UI sat at "idle" while a job
//            waited behind worker capacity.
// "running"— a worker is actively compiling/running and emitting output.
// "done"   — terminal result received.
export type JobStatusState = "idle" | "queued" | "running" | "done";

// Monotonic lifecycle ordering. Reconciliation (onResolveStatus) only ADOPTS a
// pulled status if it is AHEAD of the current one, so a slow status pull never
// regresses a live event that already arrived.
const STATUS_RANK: Record<JobStatusState, number> = {
  idle: 0,
  queued: 1,
  running: 2,
  done: 3,
};

/** Map the wire JobState (from a status pull) to the hook's JobStatusState. */
function wireStateToStatus(state: JobState): JobStatusState {
  switch (state) {
    case "running":
      return "running";
    case "done":
    case "killed":
    case "error":
      return "done";
    default:
      // "queued" | "starting"
      return "queued";
  }
}

export interface UseCodeRunnerJobArgs {
  jobId: string;
  /** Override the channel name; defaults to `private-run-<jobId>`. */
  channel?: string;
  /** Called by `sendStdin`; should POST to the user's backend (which holds the token). */
  onStdin?: (chunk: string) => void | Promise<void>;
  /** Called by `kill`; should POST to the user's backend. */
  onKill?: () => void | Promise<void>;
  /**
   * Called once the soketi subscription is confirmed
   * (`pusher:subscription_succeeded`). This is the signal to fire the
   * start-handshake: subscribe FIRST, then `POST /v1/jobs/:id/start` from your
   * backend, so no output is emitted before a subscriber is listening. See
   * /docs/concepts/lifecycle#the-start-handshake.
   */
  onSubscribed?: () => void | Promise<void>;
  /**
   * Called once on subscribe to RECONCILE state for a late join. Should GET the
   * persisted JobStatus from your backend (`GET /v1/jobs/:id/status` via
   * sdk-node's `getStatus`) and return its `state`, or null if unavailable. A
   * job that already advanced past "queued" before this client subscribed missed
   * those soketi events; the hook adopts the pulled state only if it is AHEAD of
   * what it currently shows, so a live event that arrived first is never undone.
   */
  onResolveStatus?: () => Promise<JobState | null>;
}

export interface UseCodeRunnerJobResult {
  stage: StagePhase | null;
  stdout: string;
  stderr: string;
  /**
   * Live interleaved build log of the compile stage (compiled languages only),
   * reassembled in emission order from `compile_output` events. Empty for
   * interpreted languages or before compilation produces output. Kept separate
   * from stdout/stderr so consumers can render a dedicated real-time build panel.
   */
  compileOutput: string;
  result: ResultEvent | null;
  /**
   * Captured workspace artifacts, accumulated from soketi `artifact` events.
   * Each carries name/mimeType/bytes plus a presigned `url` the browser fetches
   * directly — NO bearer token (R13 trust boundary). Populated by job end.
   */
  artifacts: Artifact[];
  status: JobStatusState;
  sendStdin: (chunk: string) => void | Promise<void>;
  kill: () => void | Promise<void>;
}

/** Reassemble a string from seq-indexed chunks in ascending seq order. */
function reassemble(buffer: Map<number, string>): string {
  return [...buffer.keys()]
    .sort((a, b) => a - b)
    .map((seq) => buffer.get(seq) ?? "")
    .join("");
}

export function useCodeRunnerJob(
  args: UseCodeRunnerJobArgs,
): UseCodeRunnerJobResult {
  const { jobId, channel, onStdin, onKill, onSubscribed, onResolveStatus } =
    args;
  const pusher = usePusher();

  // No jobId yet → channelName is null and we DON'T subscribe. Subscribing to a
  // half-formed `private-run-` channel churns auth + opens connections for a job
  // that doesn't exist. The caller sets jobId once `execute` returns.
  const channelName = channel ?? (jobId ? `private-run-${jobId}` : null);

  // Keep onSubscribed/onResolveStatus in refs so they never enter the subscribe
  // effect deps — an unstable callback there would re-subscribe (and re-fire
  // start / re-pull status) every render.
  const onSubscribedRef = useRef(onSubscribed);
  const onResolveStatusRef = useRef(onResolveStatus);
  useEffect(() => {
    onSubscribedRef.current = onSubscribed;
    onResolveStatusRef.current = onResolveStatus;
  }, [onSubscribed, onResolveStatus]);

  const [stage, setStage] = useState<StagePhase | null>(null);
  const [stdout, setStdout] = useState("");
  const [stderr, setStderr] = useState("");
  const [compileOutput, setCompileOutput] = useState("");
  const [result, setResult] = useState<ResultEvent | null>(null);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  // Optimistically "queued" the moment we have a channel to subscribe to — the
  // job is already enqueued (/execute returned status:"queued"), so showing
  // "idle" until the worker's first stage event is wrong (and gets worse the
  // longer the job waits behind capacity).
  const [status, setStatus] = useState<JobStatusState>(
    channelName ? "queued" : "idle",
  );

  // Per-stream seq->chunk buffers for ordered reassembly (worker emits monotonic
  // seq across ~8KB chunks).
  const stdoutBuf = useRef<Map<number, string>>(new Map());
  const stderrBuf = useRef<Map<number, string>>(new Map());
  const compileBuf = useRef<Map<number, string>>(new Map());

  useEffect(() => {
    // Reset state for a new job/channel.
    stdoutBuf.current = new Map();
    stderrBuf.current = new Map();
    compileBuf.current = new Map();
    setStage(null);
    setStdout("");
    setStderr("");
    setCompileOutput("");
    setResult(null);
    setArtifacts([]);
    // Reset to "queued" (not "idle") when we have a channel: a new job/channel
    // is enqueued and waiting, not idle.
    setStatus(channelName ? "queued" : "idle");

    // No channel yet (no jobId) — nothing to subscribe to.
    if (!channelName) return;

    // Guards a late status-pull resolving after this effect tore down (new job,
    // unmount) — don't apply stale reconciliation to the next job's state.
    let active = true;

    const ch = pusher.subscribe(channelName);

    // Once soketi confirms the subscription: (1) fire the start-handshake, and
    // (2) reconcile against the persisted status for a late join — a job that
    // advanced past "queued" before we were listening missed those events. Adopt
    // the pulled state only if it is AHEAD of what we currently show.
    const onSubscriptionSucceeded = () => {
      void onSubscribedRef.current?.();
      const resolve = onResolveStatusRef.current;
      if (resolve) {
        void resolve()
          .then((wireState) => {
            if (!active || !wireState) return;
            const pulled = wireStateToStatus(wireState);
            setStatus((prev) =>
              STATUS_RANK[pulled] > STATUS_RANK[prev] ? pulled : prev,
            );
          })
          .catch(() => {
            // Reconciliation is best-effort; live events remain the primary path.
          });
      }
    };
    ch.bind("pusher:subscription_succeeded", onSubscriptionSucceeded);

    const onStage = (data: StageEvent) => {
      setStage(data.phase);
      // The wire `queued` stage keeps us "queued"; compiling/running mean a
      // worker is actively executing.
      setStatus(data.phase === "queued" ? "queued" : "running");
    };
    const onStdout = (data: OutputChunkEvent) => {
      stdoutBuf.current.set(data.seq, data.chunk);
      setStdout(reassemble(stdoutBuf.current));
      setStatus("running");
    };
    const onStderr = (data: OutputChunkEvent) => {
      stderrBuf.current.set(data.seq, data.chunk);
      setStderr(reassemble(stderrBuf.current));
      setStatus("running");
    };
    const onCompileOutput = (data: OutputChunkEvent) => {
      compileBuf.current.set(data.seq, data.chunk);
      setCompileOutput(reassemble(compileBuf.current));
      setStatus("running");
    };
    const onResult = (data: ResultEvent) => {
      setResult(data);
      setStatus("done");
    };
    // Best-effort convenience stream (the authoritative source is the pulled
    // RunResult). Each Artifact carries a presigned `url` the browser fetches
    // directly — no bearer token.
    const onArtifact = (data: Artifact) =>
      setArtifacts((prev) => [...prev, data]);

    ch.bind(EVENTS.stage, onStage);
    ch.bind(EVENTS.stdout, onStdout);
    ch.bind(EVENTS.stderr, onStderr);
    ch.bind(EVENTS.compileOutput, onCompileOutput);
    ch.bind(EVENTS.result, onResult);
    ch.bind(EVENTS.artifact, onArtifact);

    return () => {
      active = false;
      ch.unbind("pusher:subscription_succeeded", onSubscriptionSucceeded);
      ch.unbind(EVENTS.stage, onStage);
      ch.unbind(EVENTS.stdout, onStdout);
      ch.unbind(EVENTS.stderr, onStderr);
      ch.unbind(EVENTS.compileOutput, onCompileOutput);
      ch.unbind(EVENTS.result, onResult);
      ch.unbind(EVENTS.artifact, onArtifact);
      pusher.unsubscribe(channelName);
    };
  }, [pusher, channelName]);

  const sendStdin = useMemo(
    () => (chunk: string) => onStdin?.(chunk),
    [onStdin],
  );
  const kill = useMemo(() => () => onKill?.(), [onKill]);

  return {
    stage,
    stdout,
    stderr,
    compileOutput,
    result,
    artifacts,
    status,
    sendStdin,
    kill,
  };
}
