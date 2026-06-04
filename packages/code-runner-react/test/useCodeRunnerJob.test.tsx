import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CodeRunnerProvider } from "../src/provider.tsx";
import { useCodeRunnerJob } from "../src/useCodeRunnerJob.ts";
import { FakePusher } from "./fake-pusher.ts";

// Swap pusher-js for the in-memory fake. The async factory re-imports the same
// module instance the test uses, so FakePusher's static state is shared.
vi.mock("pusher-js", async () => {
  const mod = await import("./fake-pusher.ts");
  return { default: mod.FakePusher };
});

function wrapper({ children }: { children: ReactNode }) {
  return (
    <CodeRunnerProvider
      appKey="code-runner-key"
      host="localhost"
      port={6001}
      authEndpoint="/api/channel-auth"
    >
      {children}
    </CodeRunnerProvider>
  );
}

function activePusher(): FakePusher {
  const p = FakePusher.instances.at(-1);
  if (!p) throw new Error("no pusher instance created");
  return p;
}

beforeEach(() => {
  FakePusher.reset();
});

describe("useCodeRunnerJob", () => {
  it("subscribes to private-run-<jobId> and starts idle", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    expect(activePusher().channel("private-run-j1")).toBeDefined();
    expect(result.current.status).toBe("idle");
    expect(result.current.stdout).toBe("");
    expect(result.current.result).toBeNull();
  });

  it("reassembles stdout from seq-numbered chunks in ascending order", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    // Deliver out of order; reassembly must sort by seq.
    act(() => ch.emit("stdout", { seq: 1, chunk: "world" }));
    act(() => ch.emit("stdout", { seq: 0, chunk: "hello " }));

    expect(result.current.stdout).toBe("hello world");
    expect(result.current.status).toBe("running");
  });

  it("reassembles stderr independently from stdout", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    act(() => ch.emit("stdout", { seq: 0, chunk: "out" }));
    act(() => ch.emit("stderr", { seq: 0, chunk: "err-a" }));
    act(() => ch.emit("stderr", { seq: 1, chunk: "err-b" }));

    expect(result.current.stdout).toBe("out");
    expect(result.current.stderr).toBe("err-aerr-b");
  });

  it("reassembles the live compile_output build log separately from run output", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    // Interleaved build log arrives on its own event, out of order by seq.
    act(() => ch.emit("compile_output", { seq: 1, chunk: "error[E0308]\n" }));
    act(() => ch.emit("compile_output", { seq: 0, chunk: "Compiling main\n" }));
    // Run output stays in its own stream.
    act(() => ch.emit("stdout", { seq: 2, chunk: "run output" }));

    expect(result.current.compileOutput).toBe("Compiling main\nerror[E0308]\n");
    expect(result.current.stdout).toBe("run output");
    expect(result.current.stderr).toBe("");
  });

  it("tracks stage and flips status to running", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    act(() => ch.emit("stage", { phase: "compiling" }));
    expect(result.current.stage).toBe("compiling");
    expect(result.current.status).toBe("running");
  });

  it("captures the terminal result event and marks the job done", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    act(() =>
      ch.emit("result", {
        exitCode: 0,
        signal: null,
        timedOut: false,
        idleTimedOut: false,
        truncated: false,
        durationMs: 42,
      }),
    );

    expect(result.current.result?.exitCode).toBe(0);
    expect(result.current.result?.durationMs).toBe(42);
    expect(result.current.status).toBe("done");
  });

  it("accumulates artifact events in arrival order", () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    act(() =>
      ch.emit("artifact", {
        name: "plot.png",
        mimeType: "image/png",
        bytes: 10,
        url: "u1",
      }),
    );
    act(() =>
      ch.emit("artifact", {
        name: "data.csv",
        mimeType: "text/csv",
        bytes: 20,
        url: "u2",
      }),
    );

    expect(result.current.artifacts.map((a) => a.name)).toEqual([
      "plot.png",
      "data.csv",
    ]);
  });

  it("honors a custom channel override", () => {
    renderHook(
      () => useCodeRunnerJob({ jobId: "j1", channel: "private-run-custom" }),
      { wrapper },
    );
    const p = activePusher();
    expect(p.channel("private-run-custom")).toBeDefined();
    expect(p.channel("private-run-j1")).toBeUndefined();
  });

  it("sendStdin and kill delegate to the provided callbacks", async () => {
    const onStdin = vi.fn();
    const onKill = vi.fn();
    const { result } = renderHook(
      () => useCodeRunnerJob({ jobId: "j1", onStdin, onKill }),
      { wrapper },
    );

    await act(async () => {
      await result.current.sendStdin("a line\n");
    });
    await act(async () => {
      await result.current.kill();
    });

    expect(onStdin).toHaveBeenCalledWith("a line\n");
    expect(onKill).toHaveBeenCalledTimes(1);
  });

  it("sendStdin is a no-op (no throw) when no onStdin callback is given", async () => {
    const { result } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    await act(async () => {
      await result.current.sendStdin("ignored");
    });
    // Reaching here without throwing is the assertion.
    expect(result.current.status).toBe("idle");
  });

  it("unbinds every handler and unsubscribes on unmount", () => {
    const { unmount } = renderHook(() => useCodeRunnerJob({ jobId: "j1" }), {
      wrapper,
    });
    const p = activePusher();
    const ch = p.channel("private-run-j1")!;

    // Handlers are bound while mounted (stage/stdout/stderr/result/artifact).
    expect(ch.boundCount()).toBeGreaterThan(0);

    unmount();

    // The real teardown guarantee: every handler unbound and the channel
    // unsubscribed, regardless of how many times React re-ran the effect.
    expect(ch.boundCount()).toBe(0);
    expect(p.unsubscribed).toContain("private-run-j1");
  });

  it("resets state and resubscribes when jobId changes", () => {
    const { result, rerender } = renderHook(
      ({ jobId }: { jobId: string }) => useCodeRunnerJob({ jobId }),
      { wrapper, initialProps: { jobId: "j1" } },
    );
    const p = activePusher();

    act(() => p.channel("private-run-j1")!.emit("stdout", { seq: 0, chunk: "a" }));
    expect(result.current.stdout).toBe("a");

    rerender({ jobId: "j2" });

    expect(result.current.stdout).toBe("");
    expect(result.current.status).toBe("idle");
    expect(p.unsubscribed).toContain("private-run-j1");
    expect(p.channel("private-run-j2")).toBeDefined();
  });

  it("fires onSubscribed once soketi confirms the subscription (start-handshake)", async () => {
    const onSubscribed = vi.fn();
    renderHook(() => useCodeRunnerJob({ jobId: "j1", onSubscribed }), {
      wrapper,
    });
    const ch = activePusher().channel("private-run-j1")!;

    await act(async () => {
      ch.emit("pusher:subscription_succeeded", undefined);
    });

    expect(onSubscribed).toHaveBeenCalledTimes(1);
  });

  it("throws when used outside a CodeRunnerProvider", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      expect(() =>
        renderHook(() => useCodeRunnerJob({ jobId: "j1" })),
      ).toThrow(/within a <CodeRunnerProvider>/);
    } finally {
      spy.mockRestore();
    }
  });
});
