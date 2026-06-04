import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CodeRunnerProvider } from "../src/provider.tsx";
import { FakePusher } from "./fake-pusher.ts";

vi.mock("pusher-js", async () => {
  const mod = await import("./fake-pusher.ts");
  return { default: mod.FakePusher };
});

beforeEach(() => {
  FakePusher.reset();
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
      <CodeRunnerProvider
        appKey="k"
        host="localhost"
        authEndpoint="/auth"
      >
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
});
