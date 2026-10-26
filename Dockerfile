# Basimagen är låst med digest: samma bygge varje gång, och lagren delas med andra images på samma bas.
FROM node:26.12.1-alpine3.24@sha256:40cd011b55415bc894e45b02fdd5a5dfde056921b7d23a64c74f80497af48797
ENV NODE_ENV=production
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-fund --no-audit && npm cache clean --force
COPY src ./src
# Databasen ligger i /data, en monterad katalog. Med read_only behöver /tmp vara en tmpfs.
RUN mkdir /data && chown node:node /data
USER node
# Statussidan (/status) för Homepage; nås bara inifrån nätverket.
EXPOSE 8080
# Processen skriver /tmp/masto-rss.alive varje minut. Är filen äldre än 3 minuter står loopen still.
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
  CMD find /tmp/masto-rss.alive -mmin -3 | grep -q . || exit 1
CMD ["node", "src/main.ts"]
