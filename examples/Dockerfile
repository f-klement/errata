# The web client is built with the Node that .nvmrc pins.
ARG PYTHON_VERSION=3.12.4
FROM node:20.11.1-alpine AS web
WORKDIR /web
COPY web/ .
RUN npm ci && npm run build

FROM python:${PYTHON_VERSION}-slim
ENV API_PORT=8080 \
    API_WORKERS=4
COPY --from=web /web/dist /srv/static
COPY vendor/icons /srv/static/icons
COPY src/ /srv/app
USER 1000
HEALTHCHECK CMD ["sh", "/srv/app/healthcheck"]
CMD ["python", "/srv/app/server.py"]
