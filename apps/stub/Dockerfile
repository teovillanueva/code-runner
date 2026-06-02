# apps/stub/Dockerfile - Interactive E2E driver
#
# Uses tsx for TypeScript execution (handles .js -> .ts remapping).

FROM node:24-slim

RUN npm install -g pnpm@10

WORKDIR /app

COPY pnpm-workspace.yaml ./
COPY package.json ./
COPY pnpm-lock.yaml ./
COPY packages/contract/package.json ./packages/contract/
COPY apps/stub/package.json ./apps/stub/

RUN pnpm install --frozen-lockfile

COPY packages/ ./packages/
COPY apps/stub/ ./apps/stub/

WORKDIR /app/apps/stub

CMD ["node_modules/.bin/tsx", "src/index.ts"]
