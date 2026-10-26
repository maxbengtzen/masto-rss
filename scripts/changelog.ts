/**
 * Hanterar CHANGELOG.md (Keep a Changelog-format).
 *
 *   node scripts/changelog.ts release [version]  flyttar "## [Unreleased]" till "## [version] - datum"
 *                                                 och lägger en ny tom Unreleased-rubrik överst.
 *                                                 Version utan argument: npm_package_version
 *                                                 (satt av `npm version`, som kör detta som hook).
 *   node scripts/changelog.ts notes <version>     skriver ut versionens avsnitt (release notes).
 *   node scripts/changelog.ts check               misslyckas om Unreleased är tomt.
 */
import { readFileSync, writeFileSync } from "node:fs";

const FILE = new URL("../CHANGELOG.md", import.meta.url);
const UNRELEASED = "## [Unreleased]";

function fail(msg: string): never {
  console.error(`changelog: ${msg}`);
  process.exit(1);
}

/** Texten i ett avsnitt, från rubriken till nästa "## [". */
function section(text: string, heading: string): string | null {
  const start = text.indexOf(heading);
  if (start < 0) return null;
  const bodyStart = text.indexOf("\n", start) + 1;
  const next = text.indexOf("\n## [", bodyStart);
  return text.slice(bodyStart, next < 0 ? undefined : next + 1).trim();
}

const [command, arg] = process.argv.slice(2);
const text = readFileSync(FILE, "utf8");

if (command === "check" || command === "release") {
  const body = section(text, UNRELEASED);
  if (body === null) fail(`rubriken "${UNRELEASED}" saknas`);
  if (!body) fail("Unreleased är tomt: beskriv ändringarna innan du släpper en version");

  if (command === "release") {
    const version = arg ?? process.env.npm_package_version ?? fail("ingen version angiven");
    if (text.includes(`## [${version}]`)) fail(`${version} finns redan i CHANGELOG.md`);
    const date = new Date().toISOString().slice(0, 10);
    writeFileSync(FILE, text.replace(UNRELEASED, `${UNRELEASED}\n\n## [${version}] - ${date}`));
    console.log(`changelog: Unreleased → ${version} (${date})`);
  }
} else if (command === "notes") {
  if (!arg) fail("ange version, t.ex. notes 1.2.3");
  const body = section(text, `## [${arg.replace(/^v/, "")}]`);
  if (!body) fail(`inget avsnitt för ${arg} i CHANGELOG.md`);
  console.log(body);
} else {
  fail("användning: changelog.ts release [version] | notes <version> | check");
}
