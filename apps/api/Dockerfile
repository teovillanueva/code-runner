# apps/api/Dockerfile - Hono API gateway
#
# Runtime: node:24-slim via @hono/node-server, executed with tsx.
#
# tsx (esbuild-based TypeScript runner) handles .js -> .ts import specifier
# remapping at runtime, which is required because the @code-runner/contract
# package uses .js extensions in imports (TypeScript ESM convention) but the
# actual source files are .ts.

FROM node:24-slim AS base

RUN npm install -g pnpm@10

WORKDIR /app

# Dependency install - copy manifests first for layer caching
COPY pnpm-workspace.yaml ./
COPY package.json ./
COPY pnpm-lock.yaml ./
COPY packages/contract/package.json ./packages/contract/
COPY apps/api/package.json ./apps/api/

# Install workspace dependencies (tsx is in devDependencies of apps/api)
RUN pnpm install --frozen-lockfile

# Source code
COPY packages/ ./packages/
COPY apps/api/ ./apps/api/
COPY languages/ ./languages/

WORKDIR /app/apps/api

ENV NODE_ENV=production

EXPOSE 8080

# tsx handles .js -> .ts specifier remapping and runs src/server.ts directly.
CMD ["node_modules/.bin/tsx", "src/server.ts"]
