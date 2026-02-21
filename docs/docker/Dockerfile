FROM oven/bun:1-alpine AS deps

WORKDIR /app

COPY package.json bun.lock ./

RUN bun install --frozen-lockfile

# Stage 2: Builder
FROM oven/bun:1-alpine AS builder

# Next.js 15.5 requires npm in PATH during build
RUN apk add --no-cache nodejs npm

WORKDIR /app

COPY --from=deps /app/node_modules ./node_modules

COPY . .

ARG NODE_ENV

ENV NODE_ENV=${NODE_ENV}

RUN bun run build && mkdir -p /app/public

# Stage 3: Runner
FROM node:24.10.0-alpine3.21 AS runner

WORKDIR /app

ARG NODE_ENV

ENV NODE_ENV=${NODE_ENV}

# Create non-root user for security
RUN addgroup --system --gid 1001 nodejs && \
    adduser --system --uid 1001 nextjs

# Copy necessary files
COPY --from=builder --chown=nextjs:nodejs /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static

USER nextjs

EXPOSE 3000

ENV PORT=3000
ENV HOSTNAME="0.0.0.0"

CMD ["node", "server.js"]
