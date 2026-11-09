# Instruktioner för kodassistenter

Den här filen är skriven för dig som är en LLM eller kodassistent och hjälper till med det här
repot: att koda, testa, versionshantera, skriva release notes och publicera. Läs den helt innan du
ändrar något. Commits, kodkommentarer och CHANGELOG skrivs på svenska (commit-rubriker enligt
Conventional Commits, se nedan).

Finns filen `AGENTS.local.md` i utcheckningen, läs den också. Den beskriver ägarens egen drift och
versionshanteras inte.

## Vad det är och var allt finns

`masto-rss` läser RSS-, Atom-, RDF- och JSON-flöden och postar nya poster till Mastodon. Alla flöden
och konton körs i en och samma container. Det är en liten Go-tjänst (från v1.1.0; tidigare
TypeScript).

| Vad | Var |
|---|---|
| Startpunkt | `cmd/masto-rss/main.go`: programmet och `healthcheck` |
| Huvudloop | `internal/app`: kontroller vid start, schemaläggning, `import`, `post`, statussidans innehåll, nedstängning |
| Konfiguration | `internal/config`: miljövariabler, `FEED_<NAMN>_<FÄLT>` per flöde |
| Hämtning och tolkning | `internal/feed`: hämtning, teckenkodning, RSS/Atom/RDF (`encoding/xml`) och JSON Feed, länkar som nycklar |
| Databas | `internal/store`: SQLite (`modernc.org/sqlite`) i `/data/masto-rss.db` |
| Beslut om vad som postas | `internal/poll`: nya poster, skydden mot flod och dubbletter, utkorgen |
| Inläggens text | `internal/format`: mallen, längd enligt Mastodons regler (grafemkluster), inga omnämnanden |
| Postning | `internal/deliver` (utkorgen, omförsök) och `internal/mastodon` (API-klienten) |
| Aviseringar och status | `internal/notify` (Signal via signal-cli-rest-api), `internal/status` (`/status`, till exempel för Homepage) |
| JavaScript-semantik | `internal/js` (`Date.parse`, `trim()` m.m.) och `internal/weburl` (`new URL()`, `URLSearchParams`), så att datum, länkar och nycklar blir desamma som i TypeScript-versionen |
| Övrigt | `internal/logx` (loggrader i `TZ`), `internal/netx` (fel i loggen), `version.go` (version och User-Agent ur `VERSION`) |
| Tester | `*_test.go` bredvid koden (HTTP är låtsad: inget nätverk), påhittade flöden i `testdata/fixtures/`, TypeScript-versionens svar i `testdata/golden/`; riktiga flöden bara lokalt i `testdata/local/` (versionshanteras inte) |
| Image | `ghcr.io/maxbengtzen/masto-rss`, byggs av GitHub Actions på taggar `vX.Y.Z` |
| Version | `VERSION` |
| Release-skript | `scripts/release.sh` (släpper en version) och `scripts/changelog.sh` (anropas av `release.sh` och av GitHub Actions) |
| CI | `.github/workflows/publish.yml` |

## Regler

- **Inga riktiga flöden i repot.** Artikeltexter tillhör andra och visar vilka flöden som körs.
  Fixtures och golden-filer bygger på påhittad text; riktiga flöden får bara ligga i den
  gitignorerade `testdata/local/`.
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
- **Håll den beroendesnål.** Det enda beroendet utöver Go:s standardbibliotek är
  `modernc.org/sqlite`. Lägg inte till fler utan att fråga. Bygget sker med `CGO_ENABLED=0`, så
  inget får kräva cgo.
- **Golden-filerna är kontraktet.** `testdata/golden/` är TypeScript-versionens svar. Ändra dem inte
  för att ett test går fel; ändras ett beteende med avsikt, uppdatera filen för hand och skriv in
  skillnaden i `testdata/golden/README.md`.
- **Containern kör skrivskyddat** (`read_only: true`) som 1000:1000 i en `scratch`-image. Koden
  skriver bara till `/data` (databasen) och `/tmp` (hälsofilen).
- **Mät minnet** när något kan påverka det (nytt beroende, ny cache, större ändringar i
  tolkningen): `docker stats` och `RssAnon` i `/proc/<pid>/status` för containerns process, före
  och efter.

## Utveckla och testa

Har du inte Go lokalt, kör i Go-imagen:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27-alpine \
  sh -c 'gofmt -l . && go vet ./... && go test ./...'
```

Lägg till eller uppdatera tester för varje beteendeändring. CI kör samma Go-version som `go.mod`
anger. Kapplöpningar mellan goroutiner hittas med `go test -race ./...` (kräver cgo, till exempel i
`golang:1.27` med `CGO_ENABLED=1`).

Grafemtabellerna i `internal/format/tables.go` genereras ur Unicodes teckendatabas med
`internal/format/gen_tables.go`; se kommentaren där. Byt Unicode-version bara tillsammans med
`testdata/unicode/GraphemeBreakTest.txt`.

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
brödtexten. Release-commits skapas av `scripts/release.sh` (`chore(release): vX.Y.Z`); skriv dem
inte för hand.

## Versionering (semver)

Vad som räknas som vad. "Användaren" är den som kör containern, alltså compose-filen och kontona
det postas till.

| Typ | När | Exempel |
|---|---|---|
| **Major** (`2.0.0`) | Compose-filen måste ändras för att det ska fungera som förut | En miljövariabel byter namn eller tas bort; ett mallfält byter namn; databasen kräver ett manuellt steg; en ändring i imagen som kräver ändringar i compose |
| **Minor** (`1.1.0`) | Ny funktion som är bakåtkompatibel | Ny valfri miljövariabel; bilder med alt-text; filter; ett nytt mallfält |
| **Patch** (`1.0.1`) | Rättning eller underhåll utan synlig beteendeändring | Buggfix; uppdaterat beroende eller basimage; bättre loggtext |

Imagen taggas `X.Y.Z`, `X.Y` och `X`. Den som kör `:1` får minor och patch vid nästa
`docker compose pull`; en major kräver att taggen i compose-filen ändras.

## Release notes (CHANGELOG.md)

Release notes skrivs **löpande** i `CHANGELOG.md` under `## [Unreleased]`, i samma commit som
ändringen. Vid release flyttar `scripts/release.sh` dem till en ny versionsrubrik, och GitHub
Actions använder exakt det avsnittet som text i GitHub Release.

- Skriv på svenska, för den som kör tjänsten: vad som är annorlunda och vad man behöver göra, inte
  hur koden ändrades.
- Använd rubrikerna `### Tillagt`, `### Ändrat`, `### Fixat`, `### Borttaget` och `### Säkerhet`,
  i den ordningen, bara de som behövs.
- Nämn nya eller ändrade miljövariabler med namn och standardvärde.
- En major börjar med `### Måste göras vid uppgradering` med exakta steg (till exempel vilken rad i
  compose-filen som ska ändras).

`scripts/release.sh` stoppar om `Unreleased` är tomt.

## Släppa en version

Förutsättningar: arbetskatalogen är ren, `main` är pushad, och CI på `main` är grön
(`gh run list --limit 3`).

1. **Kontrollera `CHANGELOG.md`.** Allt som ändrats sedan förra versionen ska stå under
   `## [Unreleased]`. Jämför med `git log --oneline v$(cat VERSION)..HEAD`.
2. **Välj version** enligt tabellen ovan och **fråga ägaren** innan du fortsätter, med
   versionsnumret och release notes som underlag.
3. **Skapa versionen:**
   ```sh
   scripts/release.sh patch   # eller minor / major / ett exakt nummer som 1.2.0
   ```
   Skriptet kontrollerar att arbetskatalogen är ren och att `Unreleased` inte är tomt, och kör
   `go vet` och testerna (i Go-imagen om Go saknas). Sedan skriver det versionen i `VERSION`,
   flyttar `Unreleased` till den nya versionen med dagens datum, committar
   (`chore(release): vX.Y.Z`) och skapar en annoterad tagg `vX.Y.Z`. Avbryts det: rätta orsaken och
   kör igen.
4. **Publicera:**
   ```sh
   git push --follow-tags
   ```
5. **Följ bygget:**
   ```sh
   gh run watch "$(gh run list --branch "v$(cat VERSION)" --limit 1 --json databaseId --jq '.[0].databaseId')" --exit-status
   ```
   Jobbet `publish` kontrollerar att taggen matchar `VERSION`, bygger och pushar imagen med
   taggarna `X.Y.Z`, `X.Y` och `X`, och skapar en GitHub Release med avsnittet ur `CHANGELOG.md`.
6. **Kontrollera releasen:** `gh release view vX.Y.Z`.
7. **Driftsätt** enligt `AGENTS.local.md`, om den finns, och fråga ägaren först.

### Om något går fel

- **CI misslyckas på taggen:** ingenting har publicerats om det föll före bygget, men taggen
  finns kvar. Rätta på `main`, lägg en rad om rättningen i `Unreleased` och släpp nästa
  patch-version. Radera inte taggen.
- **Den nya versionen fungerar inte i drift:** lås compose-filen tillfälligt till förra
  versionen, till exempel `ghcr.io/maxbengtzen/masto-rss:1.0.3`, och släpp en rättning. Byt sedan
  tillbaka till `:1`. Databasen är densamma i alla 1.x, så det går att backa utan att röra den.
- **`scripts/release.sh` säger att Unreleased är tomt:** skriv release notes först (se ovan) och
  committa.

## Underhåll

- **Beroenden:** Dependabot bevakar `go.mod` (varje vecka) och GitHub Actions (varje månad). Ett
  uppdaterat beroende släpps som en patch-version.
- **Go:** `Dockerfile` bygger med `golang:1.27-alpine` och kör i `scratch`. En ny Go-version byts i
  `go.mod` och `Dockerfile` samtidigt och släpps som en patch-version. CI läser Go-versionen från
  `go.mod`.
