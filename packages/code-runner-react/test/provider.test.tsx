import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  CodeRunnerProvider,
  __resetPusherRegistry,
} from "../src/provider.tsx";
import { FakePusher } from "./fake-pusher.ts";

vi.mock("pusher-js", async () => {
  const mod = await import("./fake-pusher.ts");
  return { default: mod.FakePusher };
});

beforeEach(() => {
  FakePusher.reset();
  __resetPusherRegistry();
});

describe("CodeRunnerProvider", () => {
  it("constructs one pusher client with the soketi connection options", () => {
    render(
      <CodeRunnerProvider
        appKey="code-runner-key"
        host="soketi.example.com"
        port={6002}
        useTLS
        authEndpoint="/api/channel-auth"
        authHeaders={{ "x-csrf": "abc" }}
      >
        <div>child</div>
      </CodeRunnerProvider>,
    );

    expect(FakePusher.instances).toHaveLength(1);
    const p = FakePusher.instances[0]!;
    expect(p.key).toBe("code-runner-key");
    expect(p.opts.wsHost).toBe("soketi.example.com");
    expect(p.opts.wsPort).toBe(6002);
    expect(p.opts.wssPort).toBe(6002);
    expect(p.opts.forceTLS).toBe(true);
    expect(p.opts.authEndpoint).toBe("/api/channel-auth");
    expect(p.opts.auth).toEqual({ headers: { "x-csrf": "abc" } });
  });

  it("defaults port to 6001 and forceTLS to false", () => {
    render(
      <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
        <div />
      </CodeRunnerProvider>,
    );
    const p = FakePusher.instances[0]!;
    expect(p.opts.wsPort).toBe(6001);
    expect(p.opts.forceTLS).toBe(false);
    expect(p.opts.auth).toBeUndefined();
  });

  it("disconnects the pusher client on unmount", () => {
    const { unmount } = render(
      <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
        <div />
      </CodeRunnerProvider>,
    );
    const p = FakePusher.instances[0]!;
    expect(p.disconnected).toBe(false);
    unmount();
    expect(p.disconnected).toBe(true);
  });

  it("shares ONE client across providers with the same config (no per-render socket leak)", () => {
    // Two providers with identical connection config model what the concurrent
    // renderer does to a single provider: run the body more than once. The old
    // per-fiber `useRef` guard created one socket per render; the module
    // registry must collapse them to a single shared client.
    render(
      <>
        <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
          <div>a</div>
        </CodeRunnerProvider>
        <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
          <div>b</div>
        </CodeRunnerProvider>
      </>,
    );

    expect(FakePusher.instances).toHaveLength(1);
  });

  it("keeps the shared client alive until the last provider unmounts", () => {
    const { rerender } = render(
      <>
        <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
          <div>a</div>
        </CodeRunnerProvider>
        <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
          <div>b</div>
        </CodeRunnerProvider>
      </>,
    );
    const p = FakePusher.instances[0]!;

    // Drop one of the two providers — refcount still > 0, so it stays connected.
    rerender(
      <>
        <CodeRunnerProvider appKey="k" host="localhost" authEndpoint="/auth">
          <div>a</div>
        </CodeRunnerProvider>
      </>,
    );
    expect(p.disconnected).toBe(false);

    // Drop the last one — now it disconnects.
    rerender(<></>);
    expect(p.disconnected).toBe(true);
  });

  it("creates separate clients for different configs", () => {
    render(
      <>
        <CodeRunnerProvider appKey="k" host="host-a" authEndpoint="/auth">
          <div>a</div>
        </CodeRunnerProvider>
        <CodeRunnerProvider appKey="k" host="host-b" authEndpoint="/auth">
          <div>b</div>
        </CodeRunnerProvider>
      </>,
    );

    expect(FakePusher.instances).toHaveLength(2);
  });
});
