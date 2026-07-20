# k8shark front image — builds the React dashboard and serves it via nginx,
# which reverse-proxies /api and /ws to the hub service.
FROM node:22-alpine AS build
WORKDIR /ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY ui/ .
RUN npm run build

FROM nginx:1.27-alpine
# The official nginx image envsubst's /etc/nginx/templates/*.template at startup
# using env vars (HUB_HOST, HUB_PORT, HUB_TOKEN set by the chart). HUB_TOKEN
# must default to empty here: the entrypoint only substitutes *defined* vars,
# and an unsubstituted ${HUB_TOKEN} would be sent as a literal header.
COPY build/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /ui/dist /usr/share/nginx/html
ENV HUB_HOST=k8shark-hub HUB_PORT=8898 HUB_SCHEME=http HUB_TOKEN=""
EXPOSE 80
