// useCodeRunnerJob — subscribe to a job's private-run-<jobId> soketi channel and
// reassemble ordered stdout/stderr from seq-numbered chunks.
//
// Actions (sendStdin/kill) are NOT performed here — the browser has no bearer
// token. They delegate to caller-provided callbacks that hit the user's backend
// (which uses sdk-node).

import type {
  Artifact,
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
} as const;

export type JobStatusState = "idle" | "running" | "done";

export interface UseCodeRunnerJobArgs {
  jobId: string;
  /** Override the channel name; defaults to `private-run-<jobId>`. */
  channel?: string;
  /** Called by `sendStdin`; should POST to the user's backend (which holds the token). */
  onStdin?: (chunk: string) => void | Promise<void>;
  /** Called by `kill`; should POST to the user's backend. */
  onKill?: () => void | Promise<void>;
}

export interface UseCodeRunnerJobResult {
  stage: StagePhase | null;
  stdout: string;
  stderr: string;
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
  const { jobId, channel, onStdin, onKill } = args;
  const pusher = usePusher();

  const channelName = channel ?? `private-run-${jobId}`;

  const [stage, setStage] = useState<StagePhase | null>(null);
  const [stdout, setStdout] = useState("");
  const [stderr, setStderr] = useState("");
  const [result, setResult] = useState<ResultEvent | null>(null);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [status, setStatus] = useState<JobStatusState>("idle");

  // Per-stream seq->chunk buffers for ordered reassembly (worker emits monotonic
  // seq across ~8KB chunks).
  const stdoutBuf = useRef<Map<number, string>>(new Map());
  const stderrBuf = useRef<Map<number, string>>(new Map());

  useEffect(() => {
    // Reset state for a new job/channel.
    stdoutBuf.current = new Map();
    stderrBuf.current = new Map();
    setStage(null);
    setStdout("");
    setStderr("");
    setResult(null);
    setArtifacts([]);
    setStatus("idle");

    const ch = pusher.subscribe(channelName);

    const onStage = (data: StageEvent) => {
      setStage(data.phase);
      setStatus("running");
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
    ch.bind(EVENTS.result, onResult);
    ch.bind(EVENTS.artifact, onArtifact);

    return () => {
      ch.unbind(EVENTS.stage, onStage);
      ch.unbind(EVENTS.stdout, onStdout);
      ch.unbind(EVENTS.stderr, onStderr);
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

  return { stage, stdout, stderr, result, artifacts, status, sendStdin, kill };
}
