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
  useMemo,
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

  const pusherRef = useRef<Pusher | null>(null);

  const pusher = useMemo(() => {
    const instance = new Pusher(appKey, {
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
    pusherRef.current = instance;
    return instance;
    // Recreate the client only when connection-defining props change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appKey, host, port, useTLS, cluster, authEndpoint]);

  useEffect(() => {
    return () => {
      pusherRef.current?.disconnect();
      pusherRef.current = null;
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
