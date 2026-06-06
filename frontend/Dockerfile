# --- build stage ---
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm install
COPY . .
RUN npm run build

# --- runtime stage ---
FROM nginx:1.27-alpine
# nginx.conf is a template; BACKEND_HOST is substituted at container start.
COPY nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
ENV BACKEND_HOST=backend:8080
EXPOSE 80
# The official nginx image runs envsubst on /etc/nginx/templates/*.template into
# /etc/nginx/conf.d at startup, then launches nginx.
