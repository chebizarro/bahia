# Build stage
FROM node:20-alpine AS builder

ARG PUBLIC_BAHIA_BOOTSTRAP_RELAYS=""
ARG PUBLIC_BAHIA_SERVICE_PUBKEYS=""
ARG PUBLIC_BAHIA_WEB_BASE_VERSION="0.1.0"
ARG PUBLIC_BAHIA_GIT_COMMIT="dev"
ARG PUBLIC_BAHIA_WEB_VERSION=""
ENV PUBLIC_BAHIA_BOOTSTRAP_RELAYS=${PUBLIC_BAHIA_BOOTSTRAP_RELAYS}
ENV PUBLIC_BAHIA_SERVICE_PUBKEYS=${PUBLIC_BAHIA_SERVICE_PUBKEYS}
ENV PUBLIC_BAHIA_WEB_BASE_VERSION=${PUBLIC_BAHIA_WEB_BASE_VERSION}
ENV PUBLIC_BAHIA_GIT_COMMIT=${PUBLIC_BAHIA_GIT_COMMIT}
ENV PUBLIC_BAHIA_WEB_VERSION=${PUBLIC_BAHIA_WEB_VERSION}

WORKDIR /app

# Copy package files
COPY package.json package-lock.json* ./

# Install dependencies
RUN npm install -g npm@11 && npm install

# Copy source files
COPY . .

# Build the SvelteKit app
RUN npm run build

# Runtime stage
FROM nginx:alpine

ARG PUBLIC_BAHIA_GIT_COMMIT="dev"
ARG PUBLIC_BAHIA_WEB_VERSION=""
LABEL org.opencontainers.image.source="https://github.com/openagentsinc/bahia" \
      org.opencontainers.image.revision="${PUBLIC_BAHIA_GIT_COMMIT}" \
      org.opencontainers.image.version="${PUBLIC_BAHIA_WEB_VERSION}"

# Copy built static files
COPY --from=builder /app/build /usr/share/nginx/html

# Copy nginx configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY docker-entrypoint.d/40-bahia-bootstrap-env.sh /docker-entrypoint.d/40-bahia-bootstrap-env.sh

# Expose port 80
EXPOSE 80

# Health check
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1/ || exit 1

CMD ["nginx", "-g", "daemon off;"]
