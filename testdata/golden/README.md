# Golden-filer

Svaren TypeScript-versionen (v1.0.3, Node 26 med ICU för Unicode 17) gav, inspelade med
`scripts/golden.ts` inför omskrivningen till Go. Testerna kräver att Go-versionen ger samma:

| Fil | Vad | Test |
|---|---|---|
| `feeds.json` | Tolkade flöden (feedsmith och normaliseringen i `feed.ts`) för allt i `../fixtures/` | `internal/feed` |
| `format.json` | Inläggens text, längd och neutraliserade omnämnanden för många mallar, gränser och texter | `internal/format` |
| `config.json` | `parseDuration` och konfigurationen eller felen för många miljöer, i samma ordning | `internal/config` |
| `text.json` | `normalizeLink`, `new URL(länk, bas)`, entiteter, HTML till text, teckenkodning, `Date.parse`, tid sedan, inläggs-id | flera |
| `schema.json` | Databasens schema och `user_version` | `internal/store` |
| `runs.json` | Hela programmet mot en låtsad server: loggen, `/status`, inläggen och aviseringarna | `internal/app` |

Alla flöden i `../fixtures/` är påhittade. `rss-wordpress-exempel.xml` och `rss-cms-exempel.xml` har
samma uppbyggnad som WordPress och ett vanligt nyhets-CMS (CDATA, `content:encoded`, `dc:creator`,
spårningsparametrar, långa texter), och resten är skrivna för kantfallen.
`../unicode/GraphemeBreakTest.txt` är Unicodes egna testfall för grafemkluster (17.0.0), som
`internal/format` också kör.

Riktiga flöden versionshanteras inte, eftersom texterna tillhör andra. Den som vill kan spara egna i
`../local/fixtures/` och spela in `../local/golden/feeds.json` och `format.json` från
TypeScript-versionen (`scripts/golden.ts` med `FIXTURES` och `GOLDEN` satta).
Testerna `TestParseGoldenLocal` och `TestFormatGoldenLocal` jämför då mot dem, och hoppas annars över.

Filerna är kontraktet med den som kör tjänsten: ändra dem inte för att ett test går fel. Ändras ett
svar med avsikt uppdateras filen för hand i samma commit, och skillnaden skrivs in nedan.

## Avsiktliga skillnader

- **Användningstexterna** säger `masto-rss import …` och `masto-rss post …` i stället för
  `node src/main.ts …`. Testet byter texten i golden-filen innan det jämför.
- **En ensam surrogat** från en numerisk entitet (`&#xD800;`) blir U+FFFD. JavaScript behöll den i
  strängen, men den blev U+FFFD ändå när texten skrevs som UTF-8, så inget inlägg blir annorlunda.
- **Nyckeln för `post`** innehåller instansens adress och därmed den låtsade serverns port; testet
  räknar fram den i stället för att läsa den ur `runs.json`.

Skillnader som golden-filerna inte täcker, eftersom de gäller fel och sällsynta indata:

- Orsaken inom parentes i loggen för nätverksfel (`TypeError: fetch failed (…)`), för trasig XML
  (`MalformedError: Invalid feed format (…)`) och när statussidans port är upptagen är Go:s egen
  text, inte Node:s.
- XML tolkas med `encoding/xml` i stället för fast-xml-parser. Fälten som läses (titel, länk,
  beskrivning och så vidare) läses som rå text på samma sätt, men XML som är trasig utanför dem kan
  ge fel i den ena och inte i den andra.
- Teckenkodningar: UTF-8, UTF-16, windows-1252 (som också är vad ISO-8859-1, latin1 och ascii betyder)
  och ISO-8859-15. Andra avkodas som UTF-8, som en okänd teckenkodning gjorde förut.
- Domännamn med andra tecken än ASCII görs om till Punycode utan Unicode-normalisering.
- Antalet poster "som saknar både id och länk" (bara i debugloggen) kan skilja för poster som bara
  innehåller okända element.
- Aviseringar som är på väg när programmet avslutas (`ONCE=1`) skickas klart innan det avslutas.
- Ett intervall över 24,8 dygn fungerar; i Node blev det en timer på 1 ms.
