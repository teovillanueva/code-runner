// CodeRunnerProvider — exposes a single pusher-js client (configured to talk to
// a self-hosted soketi server) via React context.
//
// Output-only: this provider NEVER carries the code-runner bearer token. Private
// channel subscriptions are authorized by the user's own backend (authEndpoint),
// which signs with the soketi APP_SECRET via sdk-node's createChannelAuthorizer.

import Pusher from "pusher-js";
import {
  createContext,
  useContext,
  useEffect,
  type ReactNode,
} from "react";

export interface CodeRunnerProviderProps {
  /** soketi app key (public). */
  appKey: string;
  /** soketi host, e.g. "localhost" or "soketi.example.com". */
  host: string;
  /** soketi port (defaults to 6001). */
  port?: number;
  /** Use TLS (wss) instead of ws. */
  useTLS?: boolean;
  /** Optional Pusher cluster; for self-hosted soketi leave unset. */
  cluster?: string;
  /**
   * Backend endpoint that authorizes private-channel subscriptions. This route
   * should call sdk-node's createChannelAuthorizer. The bearer token lives only
   * on that backend, never here.
   */
  authEndpoint: string;
  /** Extra headers sent with the authEndpoint request (e.g. a session cookie/CSRF). */
  authHeaders?: Record<string, string>;
  children: ReactNode;
}

const PusherContext = createContext<Pusher | null>(null);

// Module-level registry of pusher clients, keyed by connection config.
//
// WHY this is not a `useRef`/`useMemo` inside the component: `new Pusher()`
// opens a WebSocket in its constructor — a side effect. Under React's
// concurrent renderer a component's render function can run MULTIPLE times
// before commit, and intermediate renders may be DISCARDED (StrictMode
// double-invoke, interrupted/replayed renders, Suspense retries). Each render
// that ran `new Pusher()` opened a socket; the discarded ones never reach an
// effect, so their `disconnect()` cleanup never runs and the sockets leak —
// observed in the wild as 6+ live connections from a single committed mount.
//
// A config-keyed module registry makes client creation idempotent across every
// render attempt, StrictMode pass, and Fast Refresh remount: at most ONE socket
// exists per (appKey, host, port, …) regardless of how many times the provider
// body executes. A refcount disconnects the client when the last provider using
// that config unmounts (the entry is kept so a later remount reconnects the
// same client instead of opening a second socket).
interface RegistryEntry {
  pusher: Pusher;
  refs: number;
}
const registry = new Map<string, RegistryEntry>();

/** @internal Test-only: drop all cached clients so each test starts clean. */
export function __resetPusherRegistry(): void {
  registry.clear();
}

function configKey(p: CodeRunnerProviderProps): string {
  return JSON.stringify([
    p.appKey,
    p.host,
    p.port ?? 6001,
    p.useTLS ?? false,
    p.cluster ?? "",
    p.authEndpoint,
    p.authHeaders ?? null,
  ]);
}

function acquirePusher(key: string, make: () => Pusher): Pusher {
  let entry = registry.get(key);
  if (!entry) {
    entry = { pusher: make(), refs: 0 };
    registry.set(key, entry);
  }
  return entry.pusher;
}

export function CodeRunnerProvider(props: CodeRunnerProviderProps): JSX.Element {
  const {
    appKey,
    host,
    port = 6001,
    useTLS = false,
    cluster,
    authEndpoint,
    authHeaders,
    children,
  } = props;

  const key = configKey(props);
  // Idempotent: repeated renders (including the concurrent renderer's discarded
  // passes) all resolve to the same client; only the first opens a socket.
  const pusher = acquirePusher(
    key,
    () =>
      new Pusher(appKey, {
        wsHost: host,
        wsPort: port,
        wssPort: port,
        forceTLS: useTLS,
        enabledTransports: ["ws", "wss"],
        cluster: cluster ?? "",
        authEndpoint,
        // Spread conditionally — NEVER emit the `auth` key with an `undefined`
        // value. pusher-js's buildChannelAuth does `if ('auth' in opts)` (key
        // presence) then dereferences `opts.auth` with no guard, so `auth:
        // undefined` crashes with "Cannot use 'in' operator to search for
        // 'params' in undefined". Omitting the key uses authEndpoint defaults
        // (same-origin cookie auth).
        ...(authHeaders ? { auth: { headers: authHeaders } } : {}),
      }),
  );

  // Refcount on commit only (effects never run for discarded renders), so the
  // socket lifecycle tracks real mounts, not render churn.
  useEffect(() => {
    const entry = registry.get(key);
    if (!entry) return;
    entry.refs += 1;
    if (pusher.connection.state === "disconnected") {
      pusher.connect();
    }
    return () => {
      entry.refs -= 1;
      if (entry.refs <= 0) {
        pusher.disconnect();
      }
    };
  }, [key, pusher]);

  return (
    <PusherContext.Provider value={pusher}>{children}</PusherContext.Provider>
  );
}

/** Internal: read the pusher instance from context (throws if used outside the provider). */
export function usePusher(): Pusher {
  const pusher = useContext(PusherContext);
  if (!pusher) {
    throw new Error(
      "useCodeRunnerJob must be used within a <CodeRunnerProvider>",
    );
  }
  return pusher;
}
