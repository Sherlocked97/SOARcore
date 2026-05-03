# Image for the v1 SOARcore analyst UI status page.
#
# nginx-alpine serves three static files plus a proxy config. There is
# no build step today: vanilla HTML/CSS/JS by design until a UI-scoped
# tech-stack ADR (Q-010) is written. When that ADR lands and a build
# tool is chosen, this Dockerfile gets a build stage; the runtime
# stage stays nginx-alpine.

# syntax=docker/dockerfile:1.7
FROM nginx:1.27-alpine

# Drop the stock default site so our config is the only one served.
RUN rm -f /etc/nginx/conf.d/default.conf

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html /usr/share/nginx/html/index.html
COPY style.css  /usr/share/nginx/html/style.css
COPY app.js     /usr/share/nginx/html/app.js

EXPOSE 8080
