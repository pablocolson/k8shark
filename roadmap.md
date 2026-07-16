# Front-end roadmap

Issu d'un audit du code (`ui/src/`) croisé avec les capacités déjà exposées par le hub
(`pkg/api/types.go`, `internal/hub/{filter,facets,server}.go`). Organisé par impact/effort plutôt
que par ordre chronologique strict — piocher en haut de chaque section en premier.

## Sprint 1 — quick wins (petit effort, fort impact)

- [x] **Afficher `Stats.ByStatus`** (succès/warning/erreur) dans `StatsHeader.tsx` — déjà poussé par
      le hub toutes les 2s (`useHub.ts` le stocke déjà), zéro travail backend requis.
- [x] **Indicateur "N entrées arrivées pendant la pause"** — `useHub.ts:101-105` jette silencieusement
      les frames reçues en pause au lieu de les compter/bufferiser.
- [x] **Feedback sur filtre IFL invalide** — un typo retombe sur un prédicat "match rien" côté hub
      sans aucun signal dans `FilterBar.tsx` (table vide sans explication).
- [x] **Pills protocole additives, pas destructives** — cliquer un pill remplace tout le texte du
      filtre (`App.tsx:26-34`) au lieu d'ajouter/retirer juste la clause `protocol == x`.
- [x] **Dédupliquer `PROTO_COLORS`** (copié à l'identique dans `StatsHeader.tsx` et `TrafficTable.tsx`)
      vers une constante partagée.
- [x] **Copier-coller** sur body / headers / raw hex dans `EntryDetail.tsx` — absent partout.
- [x] **Raccourcis clavier globaux** — `/` pour focus le filtre, espace pour pause, Échap pour fermer
      le panneau detail.
- [x] **`amqp.deliverytag` dans l'autocomplete** — déjà filtrable via `filter.go:458-459`, juste
      absent de `facets.go` donc invisible dans `FilterSuggest`.
- [x] **Indicateur "showing latest N"** quand le buffer client (`MAX_ENTRIES = 2000`) tronque —
      aujourd'hui la troncature est silencieuse.

  Toutes les 9 cases ci-dessus sont implémentées et vérifiées en conditions réelles (`make dev` +
  navigateur) au 2026-07-15. Bug latent découvert au passage, hors scope, flagué séparément :
  `useHub.ts`'s `scheduleFlush` (coalescing `requestAnimationFrame`) peut rester bloqué
  indéfiniment si l'onglet passe en `document.hidden` — les entrées WS continuent d'arriver et de
  s'empiler dans `bufRef` mais ne sont plus jamais flushées vers l'état React tant que l'onglet
  n'est pas remis au premier plan (et certains navigateurs suspendent rAF plus ou moins
  indéfiniment sur un onglet caché).

## Historique & partage

- [x] **Brancher `GET /api/entries` / `GET /api/entry/{id}`** — déjà utilisés par le serveur MCP,
      jamais appelés par le front. Permet :
  - ~~hydratation de la table au chargement / après reconnexion~~ (déjà couvert par le replay WS
    existant ; non refait ici),
  - pagination "charger plus ancien" au-delà des 500 entrées rejouées par le WS — bouton "load
    older" en bas de la table, ancré sur la plus ancienne entrée affichée
    (`GET /api/entries?before=<id>&limit=200&filter=...`, nouveau `store.recentBefore` côté hub),
  - récupération d'une entrée qui est sortie du buffer client de 2000 via le permalien ci-dessous.
- [x] **Filtre + vue synchronisés dans l'URL** — `filter`/`view` sont maintenant initialisés depuis
      `location.search` et resynchronisés (`history.replaceState`) à chaque changement.
- [x] **Permalien vers une entrée précise** (`?entry=<id>`), avec fallback sur
      `GET /api/entry/{id}` si elle n'est plus dans le buffer live — vérifié en conditions réelles :
      l'entrée s'ouvre après reload même une fois sortie de la fenêtre de replay WS.

  Vérifié en conditions réelles (`make dev` + navigateur) au 2026-07-15, y compris le cas limite où
  l'ancre de "load older" a expiré du ring buffer hub (10 000 entrées ≈ 4 min à 40 rps) : le bouton
  bascule proprement sur "no more history" plutôt que d'échouer silencieusement.
- [x] **Export CSV/JSON** — bouton "export ▾" dans `FilterBar.tsx`, télécharge les entrées
      actuellement chargées côté client (`ui/src/export.ts` : `entriesToJSON`/`entriesToCSV`,
      échappement RFC 4180). Export PCAP explicitement écarté : nécessiterait de stocker les octets
      bruts par flux côté hub, hors scope de cette section (juste une capture des entrées déjà en
      buffer, pas un ré-export réseau).

  Vérifié en conditions réelles (`make dev` + navigateur) au 2026-07-16 : bouton désactivé sans
  entrée, export JSON confirmé (Blob intercepté via `URL.createObjectURL`, contenu = JSON
  pretty-printé fidèle au contrat `Entry`), export CSV confirmé (en-tête
  `id,timestamp,protocol,status,statusCode,elapsedMs,node,src,srcIp,srcPort,dst,dstIp,dstPort,summary`
  + lignes correctement formatées, repli `name.namespace`/`ip` sur les endpoints). Couvert aussi par
  `ui/src/export.test.ts` (4 tests) et un test dédié dans `FilterBar.test.tsx`.

## Service Map — le plus en retard

- [x] **Interactivité de base** : clic sur un service → filtre (`dst.name == "x" or src.name ==
      "x"`, ou `.ip` en repli si pas de nom résolu) + bascule automatique en vue liste ; tooltip au
      survol sur les nœuds (namespace, in/out, taux d'erreur) et sur les arêtes (nombre d'appels,
      latence moyenne, erreurs).
- [x] **Zoom / pan** (molette centrée sur le curseur, glisser pour déplacer, bouton "reset view")
      et **regroupement par namespace** : les nœuds sont maintenant triés par namespace avant le
      placement circulaire, donc les services d'un même namespace se retrouvent adjacents sur
      l'anneau au lieu d'être dispersés (pas un vrai layout multi-anneaux/force-directed — jugé
      suffisant vu l'effort déjà large de cette section).
- [x] Fix bug silencieux : les auto-appels sont maintenant rendus comme une boucle courbe
      au-dessus du nœud, avec un compteur (`×N`), au lieu d'être purement ignorés.
- [x] Fenêtre de construction du graphe configurable (200/500/800/1500/3000 dernières entrées,
      sélecteur dans la toolbar au lieu d'une valeur figée).
- [x] Légende namespace ↔ couleur, coin bas-gauche de la carte.

  Vérifié en conditions réelles (`make dev` + navigateur) au 2026-07-15 : clic-pour-filtrer,
  tooltips nœud/arête, zoom molette, pan glisser, reset view, boucle d'auto-appel, changement de
  fenêtre (146→170 liens en passant de 800 à 3000), tout confirmé fonctionnel.

## Champs backend déjà calculés, invisibles côté UI

Aucun de ces points ne demande de nouveau calcul serveur — juste affichage + entrée
`filter.go`/`facets.go` :

- [x] `redis.PipelineDepth` (calculé et testé côté worker, jamais affiché ni filtrable) — ajouté à
      `fieldGetter()` (`filter.go`) et à `facets.go` pour l'autocomplete.
- [x] `postgres.Portal`, `dns.Authoritative` / `RecursionAvl` — idem, `fieldGetter` + `facets.go`.
- [x] `Payload.Size` / `RowCount` — filtrables via IFL (`request.size`, `response.size`/`size`,
      `postgres.rowcount`/`rowcount`), en plus de l'affichage detail déjà existant.
- [x] Champs `L4Info` (flags TCP, MAC, durée, séquences…) — les 11 champs manquants ont maintenant
      un `fieldGetter` (`l4.srcmac`, `l4.dstmac`, `l4.ipversion`, `l4.ipflags`,
      `l4.clienttcpflags`, `l4.servertcpflags`, `l4.seqstart`, `l4.ackstart`, `l4.durationms`,
      `l4.clientpackets`, `l4.serverpackets`), cherchables sur tout le buffer.
- [x] `/metrics` → petit panneau santé dans `StatsHeader.tsx` : badge d'avertissement quand
      `Stats.BroadcastDropped > 0` (nouveau champ côté `pkg/api/types.go`), en plus du point
      rouge/vert existant.

  Vérifié via `internal/hub/filter_test.go` (fixture `richEntry()` étendue, ~20 nouveaux cas dans
  `TestCompileFilterRichFields`) et `go build/vet/test` au 2026-07-16 ; le badge santé confirmé en
  navigateur (`make dev`).

**Bug backend corrigé en passant** (pas un gap UI) : `HTTPDetail.TTFBMs` était déclaré dans le
contrat de données (`pkg/api/types.go:133`) et repris côté `ui/src/types.ts`, mais **aucun
dissecteur ne le calculait** (`pipeline.go`, `demo.go`) — champ mort de bout en bout. Corrigé :
`pipeline.go` capture l'instant du premier octet de la réponse HTTP (`br.Peek(1)` avant
`http.ReadResponse`) et calcule `TTFBMs = firstByteTime.Sub(requestTime)` ; `demo.go` génère une
TTFB synthétique cohérente. Nouveau test `TestHTTPTTFB` dans `dissect_test.go`, champ affiché dans
`EntryDetail.tsx` et filtrable via `http.ttfbms`.

## Accessibilité, thème, responsive

- [x] **Layout responsive** — media query `max-width: 860px` : le panneau detail passe de 440px
      fixe à 100% de large et s'empile *sous* la table (`.main.split { flex-direction: column }`)
      au lieu de rétrécir la vue à côté, le header et les actions du filtre passent en
      `flex-wrap`, la colonne `time` est masquée et `summary`/`source`/`dest` rétrécissent pour
      limiter le scroll horizontal résiduel.
- [x] **Mode clair** via `@media (prefers-color-scheme: light)` — toutes les couleurs de chrome
      qui étaient codées en dur hors du système de variables (dégradé du header, fonds de
      pill/chip/hover/ligne sélectionnée, thumb de scrollbar, et les couleurs SVG de la service
      map) ont été sorties en variables CSS pour que le thème clair les atteigne aussi, pas
      seulement `--bg`/`--text`.
- [x] **ARIA combobox** sur `FilterBar`/`FilterSuggest.tsx` (`role="combobox"`,
      `aria-autocomplete`, `aria-expanded`, `aria-controls`, `aria-activedescendant` ↔
      `role="listbox"`/`role="option"`) et **`role="tablist"`/`"tab"`/`"tabpanel"`** sur
      `EntryDetail.tsx`, avec navigation clavier flèches/Home/End en plus (roving `tabIndex`) —
      pas seulement les rôles ARIA sans le comportement clavier qui va avec.
- [x] **Focus visible** sur `.icon-btn`, `.pill`, `.chip`, `.tab`, `.view-switch`, plus `.toggle`/
      `.apply-btn` par cohérence et les nœuds de la service map (qui n'avaient jusqu'ici aucun
      accès clavier du tout — ajouté `tabIndex`, `role="button"`, `Enter`/`Espace` et un
      `aria-label` récapitulant les stats du nœud pour les lecteurs d'écran).
- [x] **Contraste de `--text-faint`** — recalculé (luminance relative WCAG) et éclairci de
      `#5a6577` (~3.3:1, sous le seuil AA) à `#7d89a6` (~5.5:1) en sombre, et une valeur
      équivalente `#5f6b82` (~5:1) ajoutée pour le clair. `--text-dim` vérifié déjà conforme
      (~6.3:1), inchangé.

  Vérifié en conditions réelles (`make dev` + navigateur) au 2026-07-16 : mode clair (tous les
  écrans, y compris service map et tabs du detail), responsive à 390px (empilement confirmé,
  colonne time masquée), sémantique ARIA inspectée via l'arbre d'accessibilité, navigation clavier
  des tabs testée (flèche → change l'onglet actif + le focus), focus-visible confirmé via une
  vraie touche Tab (anneau accent 2px, pas juste `element.focus()` qui ne déclenche pas
  `:focus-visible` dans Chrome).

## Plus ambitieux / plus tard

- [x] Historique de stats côté hub — `Server.statsHist` (ring buffer borné à 300 points,
      `statsHistoryCap`), nouvelle route `GET /api/stats/history`, nouveau type `api.StatsPoint`.
      Prérequis posé avant la sparkline ci-dessous.
- [x] Sparkline / graphique de débit dans le header — `ui/src/components/Sparkline.tsx` (SVG inline
      sans dépendance), alimentée par `useHub.ts`'s `statsHistory` (fetch initial de
      `/api/stats/history` + append à chaque frame stats WS, plafonné à 300 points côté client
      aussi).
- [x] Tri de colonnes dans la table (proto, statut, latence, heure, node, bytes, packets) — clic sur
      l'en-tête cycle asc → desc → aucun, flèche indiquant le sens actif.
- [x] Personnalisation des colonnes affichées (show/hide), notamment `node`/`bytes`/`packets`
      désormais disponibles en colonnes optionnelles — sélecteur "columns ▾", préférence persistée
      dans `localStorage` (`k8shark.columns`).
- [x] Coloration/pretty-print JSON dans le viewer de body (`EntryDetail.tsx`) — `tryPrettyJSON()` +
      `highlightJSON()`, avec repli sur le `<pre>` brut si le body n'est pas du JSON valide.
- [x] Couverture Raw tab pour DNS et les flows L4 génériques — `pipeline.go`/`dissect_l4.go`
      capturent maintenant les octets bruts (`rawViewFromBytes`, `flowState.captureRaw`) pour DNS,
      TCP/UDP génériques et ICMP, en plus de HTTP/Redis/Postgres/AMQP. Vérifié via
      `TestDNSAnswerDetail`/`TestTrackTCPCapturesRawPayload` (non testable en démo côté navigateur :
      `demo.go` ne passe pas par ces dissecteurs).
- [x] Diff entre deux entrées — "pin" jusqu'à 2 entrées (case à cocher par ligne dans la table),
      bouton "compare" ouvre `CompareView.tsx` : tableau de métadonnées côte-à-côte + diff ligne à
      ligne du body (algorithme LCS maison, plafonné à 400 lignes).
- [x] Vraie virtualisation de la table — migration vers `@tanstack/react-virtual` (ajout de
      dépendance validé avec l'utilisateur), technique de padding-row pour garder une sémantique
      `<table>` correcte ; l'ancien `content-visibility: auto` (qui évitait le paint mais laissait
      React remonter 2000 `<tr>`) est retiré.
- [x] Tests de composants React — infrastructure Vitest + jsdom + Testing Library mise en place
      (`vite.config.ts`, `test-setup.ts`), nouveaux tests sur `TrafficTable.tsx` (6),
      `FilterBar.tsx` (8), `EntryDetail.tsx` (7), `ServiceMap.tsx` (7) et `export.ts` (4) — 32 tests
      en plus de `filterParse.test.ts`.
- [x] Export CSV/JSON — voir "Historique & partage" ci-dessus.

  Toutes les cases ci-dessus vérifiées en conditions réelles (`make dev` + navigateur, screenshots
  et inspection DOM/réseau) et par `npm run build` + `npx vitest run` (43 tests passants) au
  2026-07-16.
