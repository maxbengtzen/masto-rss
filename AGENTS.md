# Instruktioner för kodassistenter

Den här filen är skriven för dig som är en LLM eller kodassistent och hjälper till med det här
repot: att koda, testa, versionshantera, skriva release notes och publicera. Läs den helt innan du
ändrar något. Commits, kodkommentarer och CHANGELOG skrivs på svenska (commit-rubriker enligt
Conventional Commits, se nedan).

Finns filen `AGENTS.local.md` i utcheckningen, läs den också. Den beskriver ägarens egen drift och
versionshanteras inte.

## Vad det är och var allt finns

`masto-rss` läser RSS-, Atom-, RDF- och JSON-flöden och postar nya poster till Mastodon. Alla flöden
och konton körs i en och samma container. Det är en liten Node 26-tjänst i TypeScript.

| Vad | Var |
|---|---|
| Konfiguration | `src/config.ts`: miljövariabler, `FEED_<NAMN>_<FÄLT>` per flöde |
| Hämtning och tolkning | `src/feed.ts` (feedsmith gör själva tolkningen) |
| Databas | `src/store.ts`: SQLite (`node:sqlite`) i `/data/masto-rss.db` |
| Beslut om vad som postas | `src/poll.ts`: nya poster, skydden mot flod och dubbletter, utkorgen |
| Inläggens text | `src/format.ts`: mallen, längd enligt Mastodons regler, inga omnämnanden |
| Postning | `src/deliver.ts` (utkorgen, omförsök) och `src/mastodon.ts` (API-klienten) |
| Aviseringar och status | `src/notify.ts` (Signal via signal-cli-rest-api), `src/status.ts` (`/status`, till exempel för Homepage) |
| Huvudloop | `src/main.ts`: kontroller vid start, schemaläggning, `import`, `post`, nedstängning |
| Övrigt | `src/log.ts` (loggrader i `TZ`), `src/version.ts` (version och User-Agent) |
| Tester | `test/*.test.ts` (`node:test`, `fetch` är låtsad: inget nätverk) |
| Image | `ghcr.io/maxbengtzen/masto-rss`, byggs av GitHub Actions på taggar `vX.Y.Z` |
| Release-skript | `scripts/changelog.ts` (anropas av `npm version` och av GitHub Actions) |
| CI | `.github/workflows/publish.yml` |

## Regler

- **Inga driftuppgifter i repot.** Sökvägar, värdnamn, nätverk och annat om en viss server hör hemma
  i `AGENTS.local.md` eller i driftens egen compose-fil, aldrig i repot eller i en commit.
- **Provposta aldrig på riktiga konton utan att ägaren sagt till.** Kör med `DRY_RUN=1`.
- **Fråga ägaren innan något som syns utanför repot:** push av en versionstagg (det publicerar en
  image och en GitHub Release) och driftsättning. Att committa och pusha vanliga commits till
  `main` är okej när ägaren bett om ändringen.
- **Återanvänd aldrig ett versionsnummer.** Flytta eller radera aldrig en publicerad tagg.
  Blev något fel i en release: rätta och släpp nästa patch-version.
- **Hemligheter:** inga tokens eller telefonnummer i kod, tester, commits, loggar eller CHANGELOG.
  Testerna använder påhittade värden. Skriv aldrig ut en token, inte ens delvis.
- **Håll den beroendesnål.** Det enda körberoendet är `feedsmith`. Lägg inte till fler utan att
  fråga, och lägg aldrig till ett byggsteg: koden körs direkt med Node 26:s type stripping. Det
  betyder att bara "raderbar" TypeScript-syntax fungerar (`erasableSyntaxOnly` i
  `tsconfig.json`): inga `enum`, inga `namespace`, inga parameter properties i konstruktorer, och
  lokala importer måste sluta på `.ts`.
- **Containern kör skrivskyddat** (`read_only: true`) som användaren `node`. Koden skriver bara
  till `/data` (databasen) och `/tmp` (hälsofilen).
- **Mät minnet** när något kan påverka det (nytt beroende, ny cache, större ändringar i
  tolkningen): `docker stats` och `RssAnon` i `/proc/1/status`, före och efter.

## Utveckla och testa

```sh
npm install
npm run typecheck   # tsc, inga utdatafiler
npm test            # node:test, inget nätverk
```

Lägg till eller uppdatera tester för varje beteendeändring. CI kör Node 26 (`node-version` i
`.github/workflows/publish.yml`). Har du en annan Node lokalt, kör testerna i samma image som
produktionen:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/w -w /w node:26.11.1-alpine3.24 npm test
```

### Torrköra mot riktiga flöden

Bygg imagen och kör ett varv i torrläge (`DRY_RUN=1`, `ONCE=1`). Ingenting postas, och allt som
skrivs i databasen rullas tillbaka. Med en riktig token kontrolleras också kontot (bara läsning);
skicka in den via miljön utan att skriva ut den.

```sh
docker build -t masto-rss:dev .
mkdir -p data
docker run --rm --read-only --tmpfs /tmp -v "$PWD/data:/data" \
  -e DRY_RUN=1 -e ONCE=1 -e STATUS_PORT=0 \
  -e MASTODON_URL=https://mastodon.example \
  -e FEED_NEWS_URL=https://example.com/feed/ -e FEED_NEWS_TOKEN \
  masto-rss:dev
```

Med tom databas ska varje flöde ge "nytt flöde", och `skulle posta:` visar inläggens text som de
skulle se ut. `data/` är ignorerad av git. Radera den efteråt.

## Commits

Conventional Commits med rubriken på svenska: `feat: …`, `fix: …`, `docs: …`, `chore: …`,
`test: …`, `refactor: …`. En brytande ändring får `!` (`feat!: …`) och en förklaring i
brödtexten. Release-commits skapas av `npm version` (`chore(release): vX.Y.Z`); skriv dem inte
för hand.

## Versionering (semver)

Vad som räknas som vad. "Användaren" är den som kör containern, alltså compose-filen och kontona
det postas till.

| Typ | När | Exempel |
|---|---|---|
| **Major** (`2.0.0`) | Compose-filen måste ändras för att det ska fungera som förut | En miljövariabel byter namn eller tas bort; ett mallfält byter namn; databasen kräver ett manuellt steg; en annan Node-huvudversion som kräver ändringar i compose |
| **Minor** (`1.1.0`) | Ny funktion som är bakåtkompatibel | Ny valfri miljövariabel; bilder med alt-text; filter; ett nytt mallfält |
| **Patch** (`1.0.1`) | Rättning eller underhåll utan synlig beteendeändring | Buggfix; uppdaterat beroende eller basimage; bättre loggtext |

Imagen taggas `X.Y.Z`, `X.Y` och `X`. Den som kör `:1` får minor och patch vid nästa
`docker compose pull`; en major kräver att taggen i compose-filen ändras.

## Release notes (CHANGELOG.md)

Release notes skrivs **löpande** i `CHANGELOG.md` under `## [Unreleased]`, i samma commit som
ändringen. Vid release flyttar `npm version` dem till en ny versionsrubrik, och GitHub Actions
använder exakt det avsnittet som text i GitHub Release.

- Skriv på svenska, för den som kör tjänsten: vad som är annorlunda och vad man behöver göra, inte
  hur koden ändrades.
- Använd rubrikerna `### Tillagt`, `### Ändrat`, `### Fixat`, `### Borttaget` och `### Säkerhet`,
  i den ordningen, bara de som behövs.
- Nämn nya eller ändrade miljövariabler med namn och standardvärde.
- En major börjar med `### Måste göras vid uppgradering` med exakta steg (till exempel vilken rad i
  compose-filen som ska ändras).

`npm version` stoppar om `Unreleased` är tomt.

## Släppa en version

Förutsättningar: arbetskatalogen är ren, `main` är pushad, och CI på `main` är grön
(`gh run list --limit 3`).

1. **Kontrollera `CHANGELOG.md`.** Allt som ändrats sedan förra versionen ska stå under
   `## [Unreleased]`. Jämför med `git log --oneline v$(node -p 'require("./package.json").version')..HEAD`.
2. **Välj version** enligt tabellen ovan och **fråga ägaren** innan du fortsätter, med
   versionsnumret och release notes som underlag.
3. **Skapa versionen:**
   ```sh
   npm version patch   # eller minor / major / ett exakt nummer som 1.2.0
   ```
   `preversion` kör `changelog.ts check`, typkontroll och tester. `version` flyttar `Unreleased`
   till den nya versionen med dagens datum. Sedan committar npm (`chore(release): vX.Y.Z`) och
   skapar en annoterad tagg `vX.Y.Z`. Avbryts det: rätta orsaken och kör igen.
4. **Publicera:**
   ```sh
   git push --follow-tags
   ```
5. **Följ bygget:**
   ```sh
   gh run watch "$(gh run list --branch "v$(node -p 'require("./package.json").version')" --limit 1 --json databaseId --jq '.[0].databaseId')" --exit-status
   ```
   Jobbet `publish` kontrollerar att taggen matchar `package.json`, bygger och pushar imagen med
   taggarna `X.Y.Z`, `X.Y` och `X`, och skapar en GitHub Release med avsnittet ur `CHANGELOG.md`.
6. **Kontrollera releasen:** `gh release view vX.Y.Z`.
7. **Driftsätt** enligt `AGENTS.local.md`, om den finns, och fråga ägaren först.

### Om något går fel

- **CI misslyckas på taggen:** ingenting har publicerats om det föll före bygget, men taggen
  finns kvar. Rätta på `main`, lägg en rad om rättningen i `Unreleased` och släpp nästa
  patch-version. Radera inte taggen.
- **Den nya versionen fungerar inte i drift:** lås compose-filen tillfälligt till förra
  versionen, till exempel `ghcr.io/maxbengtzen/masto-rss:1.0.0`, och släpp en rättning. Byt sedan
  tillbaka till `:1`.
- **`npm version` säger att Unreleased är tomt:** skriv release notes först (se ovan) och committa.

## Underhåll

- **Beroenden:** `npm outdated`, uppdatera i `package.json`, `npm install`, testa. En patch-version.
- **Node:** basimagen är låst i `Dockerfile` med tagg och digest. Hämta digesten för en ny 26.x med
  `docker buildx imagetools inspect node:26.x.y-alpine3.z`. En patch-version. Ett byte av
  Node-huvudversion ändrar även `engines` i `package.json`, `@types/node` och `node-version` i CI,
  och ska testas noga.
- **GitHub Actions:** Dependabot föreslår uppdateringar av npm-paket (varje vecka), Actions och
  basimagen (varje månad), men inte nya huvudversioner av Node.
