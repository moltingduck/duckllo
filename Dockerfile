FROM node:20-slim

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --omit=dev

COPY server.js watch.js ./
COPY public/ ./public/

RUN mkdir -p uploads

EXPOSE 3000

CMD ["node", "server.js"]
