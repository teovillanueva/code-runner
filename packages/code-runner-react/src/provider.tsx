// CodeRunnerProvider — lazily creates a single pusher-js client configured to
// talk to a self-hosted soketi server, and exposes it via React context.
//
// Output-only: this provider NEVER carries the code-runner bearer token. Private
// channel subscriptions are authorized by the user's own backend (authEndpoint),
// which signs with the soketi APP_SECRET via sdk-node's createChannelAuthorizer.

import Pusher from "pusher-js";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
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

  // Create the Pusher client EXACTLY ONCE via a ref-guard — NOT in `useMemo`.
  // `new Pusher()` opens a WebSocket eagerly; React StrictMode (and any future
  // React feature that may re-invoke memo factories) double-invokes a `useMemo`
  // factory, which would spawn multiple sockets and leak orphaned connections.
  // The ref-guard is StrictMode-safe: the double-rendered pass sees a non-null
  // ref and skips re-creation, so only one socket is ever made. Connection
  // params are read once at mount (a provider's host/key don't change at
  // runtime); remount with different props is not supported by design.
  const pusherRef = useRef<Pusher | null>(null);
  if (pusherRef.current === null) {
    pusherRef.current = new Pusher(appKey, {
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
    });
  }
  const pusher = pusherRef.current;

  useEffect(() => {
    // StrictMode runs a simulated unmount (cleanup → disconnect) then remounts;
    // reconnect if a prior cleanup left us disconnected so the live tree always
    // has an open socket.
    if (pusher.connection.state === "disconnected") {
      pusher.connect();
    }
    return () => {
      pusher.disconnect();
    };
  }, [pusher]);

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
