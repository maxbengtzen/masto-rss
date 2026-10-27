# masto-rss

Läser RSS-, Atom-, RDF- och JSON-flöden och postar nya poster till Mastodon. Alla flöden och konton
körs i en och samma container. Skriven i TypeScript och körs direkt av Node 26 (type stripping,
inget byggsteg), med SQLite (`node:sqlite`) för vad som redan setts och postats.

Inspirerad av [aserper/masto-rss](https://github.com/aserper/masto-rss), men skriven från grunden.
Det som skiljer: flera flöden och konton i samma process, dubblettkontroll som tål ändrade
länkar, skydd mot att ett flöde postas om i en klump, och en utkorg med `Idempotency-Key`, så att
ett omförsök aldrig blir en dubblett.

## Konfiguration

Varje flöde får ett namn, och dess inställningar heter `FEED_<NAMN>_<FÄLT>`. Globala värden gäller
alla flöden; samma namn med prefixet `FEED_<NAMN>_` gäller bara det flödet.

```yaml
environment:
  TZ: Europe/Stockholm
  MASTODON_URL: https://mastodon.nu
  FEED_FEBER_URL: https://feber.se/rss/
  FEED_FEBER_TOKEN: ${FEBER_TOKEN}
  FEED_TJOCK_URL: https://tjock.se/rss/
  FEED_TJOCK_TOKEN: ${TJOCK_TOKEN}
  FEED_TJOCK_INTERVAL: 30m
```

| Variabel | Standard | |
|---|---|---|
| `FEED_<NAMN>_URL` | – | Flödets adress (krävs) |
| `FEED_<NAMN>_TOKEN` | – | Access-token för kontot (krävs), med `write:statuses` |
| `FEED_<NAMN>_ENABLED` | `true` | `false` pausar flödet men behåller dess historik |
| `FEED_<NAMN>_LANGUAGE` | från flödet | ISO 639, till exempel `sv`. Bara per flöde, eftersom `LANGUAGE` redan är en systemvariabel. Utan värde används flödets `<language>` |
| `MASTODON_URL` | – | Instansen (krävs globalt eller per flöde) |
| `INTERVAL` | `15m` | Tid mellan hämtningar: sekunder eller till exempel `15m`, `1h30m`. Minst 60 s |
| `VISIBILITY` | `public` | `public`, `unlisted`, `private` eller `direct` |
| `TEMPLATE` | `{title}\n\n{link}` | Fält: `{title}`, `{link}`, `{summary}`, `{author}`, `{feed}` |
| `QUOTE_POLICY` | kontots standard | Vem som får citera: `public`, `followers` eller `nobody` |
| `MAX_AGE` | `48h` | Äldre poster postas inte. `0` stänger av spärren |
| `MAX_POSTS_PER_POLL` | `5` | Högst så många per hämtning; resten tas nästa varv |
| `BACKFILL` | `0` | Antal poster att posta första gången ett flöde körs, oavsett `MAX_AGE` |
| `SIGNAL_API_URL`, `SIGNAL_NUMBER` | tomma | Aviseringar via [signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api); av om tomma |
| `FAIL_NOTIFY_AFTER` | `3` | Avisera när ett flöde misslyckats så många gånger i rad |
| `STATUS_PORT` | `8080` | Port för statussidan; `0` stänger av |
| `DATA_DIR` | `/data` | Var databasen `masto-rss.db` ligger |
| `DRY_RUN` | – | `1` = visa vad som skulle postas; inget postas eller sparas |
| `ONCE` | – | `1` = ett varv och avsluta |
| `LOG_LEVEL` | `info` | `debug` visar även oförändrade flöden |

En okänd `FEED_*`-variabel, till exempel ett stavfel, stoppar starten med ett felmeddelande.

### Skydden

- **Nytt flöde** (eller ny databas): allt som redan finns i flödet markeras som sett och postas
  inte, utom de `BACKFILL` senaste.
- **Dubbletter:** en post räknas som sedd om dess GUID eller dess länk (utan `utm_*`, `fbclid`,
  `www.`, fragment och avslutande `/`) har setts förut. Varje inlägg skickas med en
  `Idempotency-Key`, och väntar ett inlägg längre än en timme på ett omförsök kontrolleras kontots
  senaste inlägg först.
- **Gamla poster:** äldre än `MAX_AGE` postas inte.
- **Avvikelse:** ser mer än hälften av ett flöde med minst tio poster nytt ut på en gång, utan att
  vara nyare än det senaste som setts i flödet (till exempel om sajten bytt adresser eller GUID),
  markeras allt som sett och inget postas. Nya poster med senare datum postas som vanligt, även om
  det är en hel sida på en gång.
- **Tak:** högst `MAX_POSTS_PER_POLL` per hämtning, äldst först.
- **Omnämnanden:** `@namn` i text från flödet blir aldrig ett omnämnande.

### Aviseringar

Med `SIGNAL_API_URL` och `SIGNAL_NUMBER` skickas ett Signal-meddelande till numret självt när:

- ett flöde har misslyckats `FAIL_NOTIFY_AFTER` gånger i rad, och när det fungerar igen
- Mastodon nekar ett inlägg (till exempel för långt) eller en token (kontot pausas)
- mer än hälften av ett flöde ser nytt ut på en gång utan att vara nyare än det som setts
- instansen byter Mastodon-version eller API-version, eller svarar med ett `Deprecation`-huvud

## Statussida och Homepage

`GET /status` på `STATUS_PORT` ger JSON med varje flödes konto, följare, senaste inlägg och fel. En
samlad ruta i [Homepage](https://gethomepage.dev):

```yaml
labels:
  homepage.name: masto-rss
  homepage.widgets[0].type: customapi
  homepage.widgets[0].url: http://masto-rss:8080/status
  homepage.widgets[0].refreshInterval: 60000
  homepage.widgets[0].display: dynamic-list
  homepage.widgets[0].mappings.items: feeds
  homepage.widgets[0].mappings.name: account
  homepage.widgets[0].mappings.label: summary
  homepage.widgets[0].mappings.target: "{url}"
```

## Köra i Docker

Imagen kör som användaren `node` och fungerar med skrivskyddat filsystem. Databasen ligger i `/data`
och hälsokontrollen skriver i `/tmp`:

```yaml
services:
  masto-rss:
    image: ghcr.io/maxbengtzen/masto-rss:1
    read_only: true
    tmpfs: [/tmp]
    volumes:
      - ./data:/data
    environment:
      # … som ovan
```

## Kommandon

```sh
node src/main.ts                                 # kör tills SIGTERM (ONCE=1: ett varv)
node src/main.ts import <namn> <fil>             # importera den gamla masto-rss:ens processed_entries.txt
node src/main.ts post [--language sv] <namn,…|alla> <text>   # posta en fri text till kontona
```

Importen gör att länkarna i den gamla botens `processed_entries.txt` räknas som sedda, så att inget
postas om efter bytet. I `post` blir `\n` en radbrytning.

## Mastodon-kompatibilitet

Senast genomgånget mot Mastodon 4.7.3 (API-version 11). Det som används:

| Endpoint | Till vad |
|---|---|
| `GET /api/v2/instance` | Längdgränser, version och `api_versions` (ingen token) |
| `GET /api/v1/apps/verify_credentials` | Att token fungerar och har `write:statuses` |
| `GET /api/v1/accounts/verify_credentials` | Kontot, följare och senaste inlägg (statussidan) |
| `POST /api/v1/statuses` | Postningen, med `Idempotency-Key`, `visibility`, `language` och vid API-version 7 eller senare `quote_approval_policy` |
| `GET /api/v1/accounts/:id/statuses` | Kontrollen av ett inlägg som väntat längre än en timme |

Andra servrar med Mastodons API (till exempel GoToSocial) bör fungera. Saknar instansen
`api_versions` räknas länkar i full längd, för säkerhets skull.

## Utveckling

```sh
npm install
npm run typecheck
npm test
```

Mer om test, torrkörning och releaser står i [AGENTS.md](AGENTS.md).

## Licens

[AGPL-3.0](LICENSE). Kör du en ändrad version som en tjänst åt andra ska källkoden till den finnas
tillgänglig för dem.
