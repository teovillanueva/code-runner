# apps/stub/Dockerfile — Interactive E2E driver
#
# Runs the stub script that drives a full interactive Python execute:
#   POST /v1/execute → subscribe soketi → POST /start → send stdin → assert result

FROM node:24-slim

RUN npm install -g pnpm@10

WORKDIR /app

# Copy workspace manifests for caching
COPY pnpm-workspace.yaml ./
COPY package.json ./
COPY pnpm-lock.yaml ./
COPY packages/contract/package.json ./packages/contract/
COPY apps/stub/package.json ./apps/stub/

# Install dependencies
RUN pnpm install --frozen-lockfile

# Copy source
COPY packages/ ./packages/
COPY apps/stub/ ./apps/stub/

WORKDIR /app/apps/stub

CMD ["node", "--experimental-strip-types", "src/index.ts"]
