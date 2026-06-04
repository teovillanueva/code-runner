// A minimal in-memory stand-in for pusher-js, just enough for the provider +
// useCodeRunnerJob hook: subscribe/unsubscribe/disconnect plus an `emit` test
// helper to push channel events. Wired in via `vi.mock("pusher-js", ...)`.

type Handler = (data: unknown) => void;

export class FakeChannel {
  readonly name: string;
  readonly handlers = new Map<string, Set<Handler>>();

  constructor(name: string) {
    this.name = name;
  }

  bind(event: string, cb: Handler): void {
    const set = this.handlers.get(event) ?? new Set<Handler>();
    set.add(cb);
    this.handlers.set(event, set);
  }

  unbind(event: string, cb: Handler): void {
    this.handlers.get(event)?.delete(cb);
  }

  /** Test helper: deliver an event to all currently-bound handlers. */
  emit(event: string, data: unknown): void {
    for (const cb of this.handlers.get(event) ?? []) cb(data);
  }

  /** Total bound handlers across all events — used to assert teardown. */
  boundCount(): number {
    let n = 0;
    for (const set of this.handlers.values()) n += set.size;
    return n;
  }
}

export class FakePusher {
  static instances: FakePusher[] = [];
  static reset(): void {
    FakePusher.instances = [];
  }

  readonly key: string;
  readonly opts: Record<string, unknown>;
  readonly channels = new Map<string, FakeChannel>();
  readonly unsubscribed: string[] = [];
  disconnected = false;

  constructor(key: string, opts: Record<string, unknown>) {
    this.key = key;
    this.opts = opts;
    FakePusher.instances.push(this);
  }

  subscribe(name: string): FakeChannel {
    const existing = this.channels.get(name);
    if (existing) return existing;
    const ch = new FakeChannel(name);
    this.channels.set(name, ch);
    return ch;
  }

  unsubscribe(name: string): void {
    this.unsubscribed.push(name);
  }

  disconnect(): void {
    this.disconnected = true;
  }

  /** Test helper: the channel by name (after the hook subscribed). */
  channel(name: string): FakeChannel | undefined {
    return this.channels.get(name);
  }
}
