---
"@teovilla/code-runner-react": patch
---

Fix the React SDK pusher-js crash when no auth headers are supplied (`auth: undefined` tripped pusher-js's `'auth' in opts` check). Also avoid subscribing to a `private-run-` channel before a `jobId` exists, and add an `onSubscribed` callback to `useCodeRunnerJob` to fire the start-handshake once the soketi subscription is confirmed.
