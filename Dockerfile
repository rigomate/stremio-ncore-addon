FROM golang:1.24-alpine AS go-build
WORKDIR /app
COPY torrent-server/go.mod ./go.mod
COPY torrent-server/go.sum ./go.sum
RUN go mod download
COPY ./torrent-server ./
RUN CGO_ENABLED=0 GOOS=linux go build -o ./

FROM node:20.16.0-alpine AS node-base
WORKDIR /app

COPY package.json pnpm-lock.yaml .npmrc pnpm-workspace.yaml ./
COPY ./patches ./patches
COPY ./server/package.json ./server/package.json
COPY ./client/package.json ./client/package.json


FROM node-base AS build-base
# Install the Node.js package directly; newer pnpm bootstrappers may try to
# download a standalone executable unavailable on Alpine ARM64.
RUN npm install -g pnpm@9.12.2

FROM build-base AS prod-deps
RUN pnpm install --frozen-lockfile --prod

FROM build-base AS build-deps
RUN pnpm install --frozen-lockfile

FROM build-deps AS build
COPY . .
RUN pnpm run build:server
RUN pnpm run build:client

FROM node-base AS runtime
COPY --from=prod-deps /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY --from=go-build /app/torrent-server ./dist/torrent-server/torrent-server

ENV NODE_ENV="production"
ENV ADDON_DIR="/addon"
EXPOSE 3000 3443

CMD ["node", "dist/server/index.js"]
