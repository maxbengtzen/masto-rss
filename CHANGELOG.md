# Ändringslogg

Alla ändringar som märks för den som kör tjänsten. Formatet följer
[Keep a Changelog](https://keepachangelog.com/sv/1.1.0/) och versionerna
[Semantic Versioning](https://semver.org/lang/sv/). Hur en version släpps står i [AGENTS.md](AGENTS.md).

## [Unreleased]

## [1.1.2] - 2026-11-13

### Säkerhet
- Byggd med Go 1.27.4, som rättar CVE-2026-97054 i standardbiblioteket. Go-versionen i
  Dockerfile är nu låst, så att Dependabot föreslår nya Go-patchar.

## [1.1.1] - 2026-11-10

### Fixat
- 1.1.0 publicerades aldrig: testerna föll i CI på det nya versionsnumret. 1.1.1 innehåller samma
  ändringar som står under 1.1.0 nedan.

## [1.1.0] - 2026-11-10

### Ändrat
- masto-rss är omskriven i Go. Den gör samma sak som förut, med samma miljövariabler, mallfält,
  loggtexter, aviseringar och `/status`, och den befintliga databasen används som den är. Inget
  behöver ändras i compose-filen.
- Imagen är på 13 MB i stället för 184 MB, och tjänsten använder runt 13 MiB minne i stället för
  40–45 MiB. Den bygger på `scratch` och kör som användaren 1000:1000 (samma uid som förut).
- Kommandona heter `masto-rss import …` och `masto-rss post …`. I en körande container:
  `docker exec masto-rss /masto-rss import <namn> <fil>`, i stället för `node src/main.ts …`.
- Hälsokontrollen är `masto-rss healthcheck`, med samma regel som förut: hälsofilen i `/tmp` ska
  vara yngre än tre minuter. Med `read_only: true` behöver `/tmp` fortfarande vara en tmpfs.
- Orsaken inom parentes i loggen för nätverksfel och trasig XML är formulerad annorlunda.
- Teckenkodningar som inte är UTF-8, UTF-16, ISO-8859-1/windows-1252 eller ISO-8859-15 läses som UTF-8.

### Fixat
- Aviseringar som skickas precis innan masto-rss avslutas (till exempel med `ONCE=1`) kommer fram.

## [1.0.3] - 2026-10-29

### Ändrat
- Imagen bygger på den rörliga taggen `node:26-alpine` i stället för en låst 26.x-version, så
  varje release får senaste Node 26 och Alpine. Inget behöver ändras i compose-filen.

## [1.0.2] - 2026-10-27

### Fixat
- Avvikelsespärren slog till när en sajt publicerade lika många nya poster som flödet rymmer
  på en gång (till exempel en hel morgons artiklar i ett flöde med tio poster), och då postades
  ingenting. Nu räknas bara nya poster som inte är nyare än det senaste som setts i flödet som
  misstänkta, så ett verkligt byte av adresser eller GUID stoppas fortfarande. Databasen uppgraderas
  automatiskt vid start (schemaversion 3). Behöver du backa till 1.0.1 måste schemaversionen först
  sättas tillbaka till 2.

## [1.0.1] - 2026-10-26

### Ändrat
- Källkoden är publik och licensierad under AGPL-3.0. Tjänsten fungerar som förut.

## [1.0.0] - 2026-10-26

Första versionen. Ersätter en container per flöde med `ghcr.io/aserper/masto-rss` med en enda
container för alla flöden och konton.

### Tillagt
- Läser RSS-, Atom-, RDF- och JSON-flöden och postar nya poster till Mastodon. Flera flöden och
  konton körs i samma container, med inställningar per flöde som `FEED_<NAMN>_<FÄLT>` och globala
  standardvärden (se README). En okänd `FEED_*`-variabel stoppar starten.
- Dubblettkontroll på både GUID och städad länk (utan `utm_*`, `fbclid`, `www.`, fragment och
  avslutande `/`), sparad i SQLite i `/data/masto-rss.db`.
- Skydd mot flod: ett nytt flöde postar inget (utom `BACKFILL`, standard `0`), poster äldre än
  `MAX_AGE` (standard `48h`) postas inte, ser mer än hälften av ett flöde nytt ut på en gång postas
  inget, och högst `MAX_POSTS_PER_POLL` (standard `5`) postas per varv, äldst först.
- Utkorg: ett inlägg sparas innan det skickas och får en `Idempotency-Key`, så att ett omförsök
  aldrig blir en dubblett. Tillfälliga fel provas igen efter 1, 5, 15, 60 och sedan 360 minuter,
  vid 429 när Mastodons fönster öppnar. Ett inlägg som Mastodon nekar (till exempel 422) provas
  inte om, och en token som nekas pausar bara sitt eget konto.
- Texten byggs av mallen `TEMPLATE` (standard `{title}\n\n{link}`) och kortas så att den ryms enligt
  Mastodons sätt att räkna. `@namn` i text från flödet blir aldrig ett omnämnande.
- Kontroll vid start av instansen (`/api/v2/instance`) och varje token (scopes och konto). Byter
  instansen version eller `api_versions`, och när Mastodon svarar med ett `Deprecation`-huvud,
  syns det i loggen och i en avisering.
- Signal-aviseringar via signal-cli-rest-api (`SIGNAL_API_URL`, `SIGNAL_NUMBER`): när ett flöde misslyckats
  `FAIL_NOTIFY_AFTER` gånger i rad (standard `3`) och när det fungerar igen, när Mastodon nekar ett
  inlägg eller en token, och vid avvikelser.
- Statussida på `http://<container>:8080/status` (`STATUS_PORT`, `0` stänger av) för en samlad
  Homepage-ruta med följare och senaste inlägg per konto.
- Villkorlig hämtning med ETag och Last-Modified, timeout på 30 s, storleksgräns på 5 MB och ett tips
  i loggen när en flödesadress har flyttat permanent. Efter fel växer väntetiden upp till 6 h.
- `node src/main.ts import <namn> <fil>` importerar den gamla masto-rss:ens `processed_entries.txt`,
  och `node src/main.ts post <namn,…|alla> <text>` postar en fri text till flödenas konton.
- Torrläge (`DRY_RUN=1`) och ett enda varv (`ONCE=1`).
- Hälsokontroll i imagen. Med `read_only: true` behöver `/tmp` vara en tmpfs.
- Imagen bygger på Node 26 (`node:26.11.1-alpine3.24`).
