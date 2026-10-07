# MCParcel

Eén CLI voor alle MCP-aanroepen, met een centraal onderhouden teamcatalogus,
persoonlijke toolselecties en credentials uit 1Password.

MCParcel wordt een zelfstandige vervanger van mcporter. Agents en harnesses
gebruiken dezelfde CLI; serverconfiguratie en authenticatie hoeven niet in elke
AI-client afzonderlijk onderhouden te worden.

## Beoogde ervaring

Onderstaande commando's beschrijven de gewenste interface. Ze bestaan nog niet.

```sh
npx mcparcel add acme/mcp-catalog
npx mcparcel setup
npx mcparcel list
npx mcparcel call <server>.<tool> key=value
```

`add` koppelt de GitHub-repository als catalogusbron. `setup` opent een terminalinterface
waarin je per domein, zoals Design of Servers, MCP's uit catalogi of lokale configuratie
activeert. De eerste aanroep die credentials nodig heeft, regelt de authenticatie;
een expliciet unlockcommando hoort niet bij de normale workflow.

`setup` werkt volledig met het toetsenbord: pijltjes om te navigeren, spatie om een MCP
aan of uit te zetten, Enter voor details, Ctrl+S om te bewaren en `?` voor alle toetsen.

De voorbeelden gebruiken `npx` en vereisen geen globale installatie. Een blijvende
native installatie met een rechtstreeks `mcparcel`-commando blijft mogelijk; de
precieze distributiewijze moet nog worden uitgewerkt.

In de globale `AGENTS.md` volstaat één instructie:

> Gebruik mcparcel voor alle MCP-aanroepen.

- Het team onderhoudt serverdefinities en secretverwijzingen centraal.
- Iedere collega kiest welke servers en tools actief zijn en kan eigen servers toevoegen.
- Lokale MCP's, zoals Paper, blijven op de eigen laptop werken.
- Credentials worden alleen opgehaald wanneer de gekozen server ze nodig heeft.
- Na een eenmalige autorisatie kan een werksessie zonder herhaalde biometrische prompts doorgaan.

De CLI en terminalinterface worden volledig Engelstalig.

## Documentatie

Begin bij de [roadmap](docs/superpowers/plans/2026-10-04-mcparcel.md). Die bevat
de keuzes, elf bouwstappen, bestanden, tests en acceptatiepoorten. Elke stap krijgt
een eigen gedetailleerd plan; het
[plan voor stap 1](docs/superpowers/plans/2026-10-04-mcparcel-stage-1-feasibility.md)
is klaar om uit te voeren. Publicatie vraagt een afzonderlijke opdracht.

- [CLI-contract en terminalschets](docs/cli.md): commando’s en Engelstalige interface.
- [Catalogus en persoonlijke configuratie](docs/catalog.md): JSON-model, bronnen, merge en import.
- [Runtime en authenticatie](docs/runtime.md): daemon, 1Password, OAuth, sessies en procesbeheer.
- [Compatibiliteitsbasis](docs/compatibility.md): alle 32 huidige MCP’s moeten bruikbaar blijven.
- [Onderzoeksbasis](docs/research.md): primaire bronnen, gecontroleerde feiten en nog te bewijzen aannames.
- [Productrichting](docs/product.md): oorspronkelijke behoeften en productgrenzen.

Het bouwplan beveelt Go op macOS arm64 aan, met API-keys in 1Password en persoonlijke
OAuth-sessies in Keychain. Die OAuth-opslag is een expliciete ontwerpkeuze ter review.
Een lokale sessie van 24 uur garandeert geen onmiddellijke intrekking bij offboarding.

## Status

Release candidate `0.1.0-rc.1`. It installs from a local tarball and is not
published to npm. The npm package supports macOS on Apple Silicon only; Linux
runs headless from a static binary (see [docs/headless.md](docs/headless.md)).
The macOS binary is ad-hoc signed. It is not Developer ID signed or notarized.

## Install from a local tarball

You need Node.js 20 or newer; Go and Homebrew are not needed to run it.

```sh
make npm-pack
npx --yes --package ./dist/mcparcel-0.1.0-rc.1.tgz mcparcel doctor
# or install it globally
npm install -g ./dist/mcparcel-0.1.0-rc.1.tgz
```

Upgrading, rolling back and returning to mcporter are described in
[docs/migration.md](docs/migration.md).
