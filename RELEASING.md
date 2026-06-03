# Releasing the `@teovilla/*` packages

This repo publishes three public npm packages with [Changesets](https://github.com/changesets/changesets):

| Package | What it is |
|---|---|
| `@teovilla/code-runner-contract` | Shared wire types + helpers (transitive dep of the two SDKs) |
| `@teovilla/code-runner-sdk-node` | Server-side SDK for the gateway + soketi channel-auth signing |
| `@teovilla/code-runner-react` | Browser real-time SDK (pusher-js wrapper) |

The private apps (`@code-runner/api`, `@code-runner/stub`) and the root package are never published.

## Day-to-day flow

1. Make your change in a branch.
2. Add a changeset describing the release:
   ```bash
   pnpm changeset
   ```
   Pick the affected packages and bump type (patch / minor / major), write a one-line summary, and
   commit the generated `.changeset/*.md` file with your PR.
3. Merge your PR to `main`.
4. The **`release`** workflow opens a **"Version Packages"** PR that applies the changesets (bumps
   versions, updates `CHANGELOG.md`, rewrites internal `workspace:*` ranges to real versions).
5. Merge the "Version Packages" PR. The `release` workflow runs again and **publishes** the bumped
   packages to npm (in dependency order — contract first), with [provenance](https://docs.npmjs.com/generating-provenance-statements).

## Canary / snapshot releases

Manually trigger the **`canary`** workflow (Actions → canary → Run workflow). With pending
changesets present, it publishes throwaway pre-releases under the `canary` dist-tag:

```bash
npm i @teovilla/code-runner-sdk-node@canary   # bleeding edge
npm i @teovilla/code-runner-sdk-node          # = @latest (stable)
```

---

## One-time setup

### 1. npm — create the scope

Create the `@teovilla` scope on [npmjs.com](https://www.npmjs.com/) (a free user/org scope is fine
for public packages). The packages already declare `"publishConfig": { "access": "public" }`.

### 2. npm — OIDC Trusted Publishing (no tokens)

We authenticate CI to npm with GitHub OIDC, so there's **no `NPM_TOKEN` secret to manage**. For
**each** of the three packages, on npmjs.com → package **Settings → Trusted Publisher → GitHub
Actions**, set:

- **Repository:** `teovillanueva/code-runner`
- **Workflow filename:** `release.yml` (and `canary.yml` if you want canary publishes)
- **Environment:** leave empty (we don't gate on a GitHub Environment)

CI requires npm ≥ 11.5.1 (the workflow upgrades npm) and the `id-token: write` permission (already
set in the workflows).

> **Bootstrap (first publish only).** A trusted publisher can only be attached to a package that
> already exists on npm. For the very first publish of each brand-new name, do one manual publish
> from your machine, then configure the trusted publisher for CI thereafter:
> ```bash
> pnpm --filter "@teovilla/*" build
> npm publish -w @teovilla/code-runner-contract --access public
> npm publish -w @teovilla/code-runner-sdk-node --access public
> npm publish -w @teovilla/code-runner-react   --access public
> ```
> (Publish the contract first — the SDKs depend on it.)
>
> Prefer not to publish manually? You can instead use a short-lived **Automation token**: add it as
> the `NPM_TOKEN` repo secret and temporarily give the publish step
> `--//registry.npmjs.org/:_authToken`. Once the names exist, switch to OIDC and delete the secret.

### 3. GitHub — repo settings

**Settings → Actions → General → Workflow permissions:**

- Select **Read and write permissions**.
- Tick **Allow GitHub Actions to create and approve pull requests** (Changesets opens the
  "Version Packages" PR).

No other secrets are required for the OIDC path.

---

## Docker images (separate from the npm packages)

The npm SDKs and the **service container images** are different artifacts with independent
lifecycles — don't couple their versions. Images are published to GHCR by
`.github/workflows/release-images.yml`:

| Image(s) | Built when | Tags |
|---|---|---|
| `code-runner-api`, `code-runner-worker` | every push to `main` | `latest` + `sha-<short>` (immutable) |
| `code-runner-api`, `code-runner-worker` | a `vX.Y.Z` git tag | `1`, `1.2`, `1.2.3` |
| `executor-python` / `-rust` / `-r` / `-sqlite` | `languages/**` changed, a tag, or manual run | `<version>` + `<version>-<sha>` |

- `latest` tracks `main` HEAD (continuous). For reproducible deploys, pin a `sha-<short>` tag.
- **Cut a stable release** with a git tag — no extra tooling:
  ```bash
  git tag v1.2.3 && git push origin v1.2.3
  ```
- Auth uses the workflow's automatic `GITHUB_TOKEN` (`packages: write`) — no secrets.
- One-time: set the GHCR packages to **public** (each package → Settings → Change visibility)
  so self-hosters can pull without authenticating.
