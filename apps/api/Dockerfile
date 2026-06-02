# apps/api/Dockerfile — Hono API gateway
#
# Runtime: node:24-slim via @hono/node-server
# Entrypoint: node --experimental-strip-types src/server.ts
#   (native TS stripping, no build step required)

FROM node:24-slim AS base

# Install pnpm globally
RUN npm install -g pnpm@10

WORKDIR /app

# ── Dependency install (all workspace deps needed for the API) ────────────────
# Copy workspace manifests first for better layer caching
COPY pnpm-workspace.yaml ./
COPY package.json ./
COPY pnpm-lock.yaml ./
COPY packages/contract/package.json ./packages/contract/
COPY apps/api/package.json ./apps/api/

# Install workspace dependencies
RUN pnpm install --frozen-lockfile

# ── Source code ────────────────────────────────────────────────────────────────
COPY packages/ ./packages/
COPY apps/api/ ./apps/api/
COPY languages/ ./languages/

# ── Runtime ────────────────────────────────────────────────────────────────────
WORKDIR /app/apps/api

ENV NODE_ENV=production

# Expose the API port (overridden at runtime via API_PORT env var)
EXPOSE 8080

# Run with experimental strip-types so .ts files load without a compile step.
# All config comes from environment variables.
CMD ["node", "--experimental-strip-types", "src/server.ts"]
