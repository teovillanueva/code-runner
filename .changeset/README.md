# Changesets

This folder is managed by [changesets](https://github.com/changesets/changesets). It drives the
versioning and publishing of the public `@teovilla/*` packages (`code-runner-contract`,
`code-runner-sdk-node`, `code-runner-react`). The private apps (`@code-runner/api`,
`@code-runner/stub`) are ignored and never published.

## Adding a changeset

When you make a change that should ship in a release, run:

```bash
pnpm changeset
```

Pick the affected packages and the bump type (patch / minor / major), and write a short summary.
This creates a markdown file in `.changeset/` that you commit alongside your change.

## How a release happens

On push to `main`, the `release` workflow runs `changesets/action`:

1. If there are pending changesets, it opens (or updates) a **"Version Packages"** PR that bumps
   versions and updates changelogs.
2. When that PR is merged, the same workflow publishes the bumped packages to npm.

See [`RELEASING.md`](../RELEASING.md) for the full flow and the one-time npm/GitHub setup.
