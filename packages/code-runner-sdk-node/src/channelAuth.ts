// Zero-dependency soketi private-channel auth signing.
//
// This mirrors apps/api/src/channelAuth.ts (which uses the `pusher` server SDK)
// byte-for-byte: pusher's authorizeChannel produces
//   { auth: "<appKey>:" + HMAC_SHA256(`${socket_id}:${channel_name}`, appSecret) }
// where the HMAC is rendered as lowercase hex.
//
// The soketi APP_SECRET only ever lives server-side; this module is part of the
// node SDK and must never be bundled into a browser package.

import { createHmac } from "node:crypto";

export interface SignChannelAuthArgs {
  /** pusher-js socket id, e.g. "123.456". */
  socketId: string;
  /** Channel being authorized; must be a `private-run-<jobId>` channel. */
  channelName: string;
  /** soketi app key (public). */
  appKey: string;
  /** soketi app secret (private, server-side only). */
  appSecret: string;
}

export interface ChannelAuthResponse {
  /** The `auth` token pusher-js expects in the authorizer response. */
  auth: string;
}

/**
 * Produce the pusher private-channel auth response for a socket + channel.
 * Output is byte-identical to apps/api's pusher.authorizeChannel.
 */
export function signChannelAuth(args: SignChannelAuthArgs): ChannelAuthResponse {
  const { socketId, channelName, appKey, appSecret } = args;
  const signature = createHmac("sha256", appSecret)
    .update(`${socketId}:${channelName}`)
    .digest("hex");
  return { auth: `${appKey}:${signature}` };
}

export interface CreateChannelAuthorizerArgs {
  appKey: string;
  appSecret: string;
}

/**
 * Build an authorizer `(socketId, channelName) => { auth }` suitable for wiring
 * into a backend `/channel-auth` route. Refuses any channel that is not a
 * `private-run-*` channel, mirroring the apps/api guard.
 */
export function createChannelAuthorizer(
  args: CreateChannelAuthorizerArgs,
): (socketId: string, channelName: string) => ChannelAuthResponse {
  const { appKey, appSecret } = args;
  return (socketId: string, channelName: string): ChannelAuthResponse => {
    if (!channelName.startsWith("private-run-")) {
      throw new Error(
        `Refusing to authorize channel "${channelName}": only private-run-<jobId> channels are allowed`,
      );
    }
    return signChannelAuth({ socketId, channelName, appKey, appSecret });
  };
}
