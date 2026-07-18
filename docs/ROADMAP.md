# Roadmap d'amélioration k8shark

> Généré le 2026-07-17 par un audit multi-agents (8 dimensions, chaque proposition
> vérifiée contre le code avec preuve fichier:ligne). 76 propositions retenues
> (67 confirmées, 9 ajustées après vérification) + 6 angles morts identifiés par
> un critique de complétude. Les chantiers déjà en cours au moment de l'audit
> (pause/reprise de capture par worker, export PCAP côté client, vue workers)
> sont volontairement exclus.

Légende : impact fort/moyen/faible ; effort S (<1 j), M (1-3 j), L (>3 j).
Statut `ajusté` = idée retenue mais constat ou approche corrigé par le vérificateur (voir la note).

## Phases proposées

### Phase 0 - Quick wins (~1 semaine)

Bugs avérés et garde-fous à très bon ratio valeur/effort.

- **DIS-2** Bug : les réponses aux requêtes HEAD désynchronisent tout le flux réponse (impact fort, S (<1 j))
- **DIS-3** Bug : les réponses intérimaires 1xx (Expect: 100-continue) décalent l'appariement FIFO (impact fort, S (<1 j))
- **OPS-1** Stamping de version cassé : mauvais chemin de module dans -ldflags et VERSION jamais passé en CI (impact fort, S (<1 j))
- **OPS-4** k8shark clean supprime le namespace entier sans garde-fou (impact fort, S (<1 j))
- **CAP-2** Pertes de paquets du ring AF_PACKET invisibles : tp.SocketStats() jamais lu (impact fort, S (<1 j))
- **CAP-3** Réassemblage TCP sans borne mémoire : risque d'OOMKill du DaemonSet (impact fort, S (<1 j))
- **CAP-6** Map des flows L4 non bornée : un port scan gonfle la mémoire du worker (impact moyen, S (<1 j))
- **TST-1** Exécuter les tests vitest du front dans la CI (impact fort, S (<1 j))
- **TST-4** Activer le détecteur de course (-race) dans la CI (impact moyen, S (<1 j))

### Phase 1 - Sûr par défaut (~1 semaine)

Corriger les défauts de sécurité par défaut avant d'élargir l'audience. OPS-5 (NetworkPolicy) fusionne avec SEC-4.

- **SEC-1** Génération automatique d'un token API par « k8shark tap » (auth désactivée par défaut) (impact fort, S (<1 j))
- **SEC-3** Worker : privileged: false par défaut, le chart liste déjà les capabilities fines (impact fort, S (<1 j))
- **SEC-4** Ajouter des NetworkPolicy au chart (hub joignable par n'importe quel pod) (impact fort, S (<1 j))
- **SEC-2** Redaction des secrets Redis AUTH, params Bind Postgres et query params HTTP sensibles (impact fort, M (1-3 j))
- **HUB-8** Garde-fou multi-réplicas : le chart accepte hub.replicas > 1 alors que tout l'état est local au pod (impact moyen, S (<1 j))

### Phase 2 - Produit court terme (2 a 3 semaines)

Forte valeur utilisateur pour un effort modéré ; plusieurs items ne font qu'exploiter des endpoints hub existants.

- **CAP-1** Filtre BPF kernel hardcodé : les ports configurés par l'opérateur ne sont jamais capturés (impact fort, M (1-3 j))
- **HUB-2** IFL : opérateurs regex (matches), liste (in) et startswith (impact fort, M (1-3 j))
- **UI-1** Ancrage du scroll pendant le streaming (lecture sans gel manuel) (impact fort, M (1-3 j))
- **UI-2** Timeline/histogramme cliquable exploitant /api/timeline (déjà côté hub) (impact fort, M (1-3 j))
- **UI-3** Copier en cURL (et base pour rejouer une requête HTTP) (impact moyen, S (<1 j))
- **UI-4** Navigation clavier ↑/↓ dans la table des entrées (impact moyen, S (<1 j))
- **MCP-2** Outil diff_traffic : comparaison de deux fenêtres temporelles (impact fort, S (<1 j))
- **MCP-3** Outil find_error_clusters : erreurs groupées par signature (impact fort, M (1-3 j))
- **DIS-4** Bodies HTTP : décompression bornée (gzip/deflate) et rendu binaire-sûr partagé (impact fort, S (<1 j))

### Phase 3 - Gros chantiers (par itérations, dans cet ordre de valeur)

- **DIS-1** Dissecteur HTTP/2 + gRPC (h2c et via eBPF TLS) (impact fort, L (>3 j))
- **CAP-4** Go crypto/tls non hooké : la majorité du trafic TLS des workloads k8s reste opaque (impact fort, L (>3 j))
- **HUB-1** Persistance optionnelle du buffer et rétention configurable (durée et mémoire, pas seulement un compte fixe) (impact fort, L (>3 j))
- **EXT-1** Ciblage de la capture par namespace/pod (tap targeting), la fonctionnalité phare de Kubeshark absente (impact fort, L (>3 j))
- **OPS-3** Aucune release automatisée : ni binaires CLI, ni publication du chart Helm (impact fort, M (1-3 j))
- **OPS-2** Images CI en linux/amd64 uniquement : pas de support arm64 (impact fort, M (1-3 j))
- **EXT-6** Distribution en plugin kubectl via krew (et Homebrew) : kubectl shark tap (impact moyen, M (1-3 j))

Le reste des findings ci-dessous constitue le fond de backlog, à piocher par thème.

## Détail des findings par dimension

### Capture worker (AF_PACKET, eBPF, pipeline)

#### CAP-1 - Filtre BPF kernel hardcodé : les ports configurés par l'opérateur ne sont jamais capturés

*impact fort | effort M (1-3 j) | feature | confirmé*

Le filtre cBPF embarqué dans capture/afpacket_linux.go ne laisse passer que tcp 80/8080/6379/5432/5672, udp 53 et icmp. Or les flags --redis-ports, --valkey-ports et --amqp-ports (worker.go, cli/worker.go) ne modifient que le dispatch userspace : les paquets vers ces ports sont éliminés en kernel avant d'arriver au pipeline, donc la fonctionnalité est silencieusement cassée. De plus tout HTTP hors 80/8080 (8000, 3000, 9090... très courants en k8s) est invisible alors que consumeStreamID sait sniffer HTTP sur n'importe quel port. Générer le programme cBPF au runtime à partir de la liste de ports effective (golang.org/x/net/bpf permet d'assembler les instructions, pas besoin de libpcap) et ajouter un flag --http-ports ou un mode --capture-all-tcp.

Fichiers : `internal/worker/capture/afpacket_linux.go`, `internal/worker/capture/source.go`, `internal/worker/worker.go`, `internal/cli/worker.go`

#### CAP-2 - Pertes de paquets du ring AF_PACKET invisibles : tp.SocketStats() jamais lu

*impact fort | effort S (<1 j) | ops | confirmé*

WorkerStats ne rapporte que les drops du buffer sink (entries déjà dissectées) ; les drops en amont, dans le ring kernel TPACKET_V3 (afpacket.SocketStatsV3 expose packets/drops/queue-freezes), ne sont jamais lus. Sur un noeud chargé, le ring déborde et l'exploitant voit simplement moins de trafic sans aucun signal, alors que c'est la perte la plus probable en production. Ajouter une méthode Stats() à PacketSource, la sonder dans captureLoop (ticker existant), et étendre WorkerStats avec des champs additifs (ringDrops, ringPackets) remontés à /api/workers et au /metrics du hub. Effort faible, gain d'exploitabilité majeur.

Fichiers : `internal/worker/capture/afpacket_linux.go`, `internal/worker/capture/source.go`, `internal/worker/worker.go`, `internal/worker/sink.go`, `pkg/api/types.go`

#### CAP-3 - Réassemblage TCP sans borne mémoire : risque d'OOMKill du DaemonSet

*impact fort | effort S (<1 j) | robustesse | confirmé*

captureLoop crée l'assembler avec tcpassembly.NewAssembler sans fixer MaxBufferedPagesTotal ni MaxBufferedPagesPerConnection (défaut 0 = illimité). Dès que le ring kernel droppe des segments (voir finding précédent), les pages out-of-order s'accumulent pendant jusqu'à 2 minutes avant le FlushOlderThan, et un DaemonSet avec memory limit se fait OOMKill, tuant toute la capture du noeud. Fixer des bornes (par ex. MaxBufferedPagesTotal ~150k pages, PerConnection ~4k) et éventuellement raccourcir la fenêtre de flush ; deux lignes de code, comportement dégradé propre (le flux est tronqué au lieu de tuer le pod).

Fichiers : `internal/worker/worker.go`

#### CAP-4 - Go crypto/tls non hooké : la majorité du trafic TLS des workloads k8s reste opaque

*impact fort | effort L (>3 j) | feature | confirmé*

--enable-go-tls est un stub qui logge un warning (tls_pipeline.go), or l'écosystème k8s est majoritairement en Go (contrôleurs, API gateways, services gRPC) et statiquement lié : aucune libssl.so à découvrir dans /proc/<pid>/maps, donc zéro couverture. Implémenter la phase 2b : scanner les binaires ELF Go (section .gosymtab/symbole crypto/tls.(*Conn).Write et .Read), attacher des uprobes en tenant compte de l'ABI registre (Go >= 1.17) et éviter les uretprobes classiques qui crashent avec les stacks copiables des goroutines (hooker les offsets des instructions RET à la place, technique éprouvée par Pixie/Kubeshark). Les records aboutiraient dans le même consumeTLS existant, donc aucun changement pipeline.

Fichiers : `internal/worker/tls_pipeline.go`, `internal/worker/ebpf/attach.go`, `internal/worker/ebpf/loader.go`, `internal/worker/ebpf/bpf`

#### CAP-5 - Le drop-oldest du drainLoop eBPF désynchronise les flux TLS au lieu de les tronquer proprement

*impact moyen | effort M (1-3 j) | robustesse | confirmé*

En backpressure, loader.go drainLoop droppe le plus ancien record du canal out, c'est-à-dire un chunk au milieu d'un byte-stream ; c'est exactement le trou intérieur que chanPipe (tls_pipeline.go) s'interdit parce qu'il désynchronise le parseur et produit des entries corrompues pour tout le reste de la connexion. Politique incohérente entre les deux étages du même chemin de données. Correctif : à la place, marquer le ConnID du record droppé comme laggé (petit set côté drainLoop ou propagation au tlsStream) pour fermer ce flux avec une troncature propre, comme le fait déjà chanPipe.push. Compter ces drops dans les WorkerStats au passage.

Fichiers : `internal/worker/ebpf/loader.go`, `internal/worker/tls_pipeline.go`

#### CAP-6 - Map des flows L4 non bornée : un port scan gonfle la mémoire du worker

*impact moyen | effort S (<1 j) | robustesse | ajusté*

p.flows n'est purgée que par flushFlows toutes les 15 s avec 20 s d'idle : un SYN flood ou un scan (nmap sur un /16) crée des centaines de milliers de flowState (chacun avec headerHex, rawBuf, MACs...) avant la première purge, sans aucune limite, contrairement au backlog de requêtes qui est borné (reqBacklogCap). Ajouter un plafond maxFlows (par ex. 100k) avec éviction du plus ancien à l'insertion dans trackTCP/trackUDP, un compteur d'évictions exposé dans WorkerStats, et éventuellement ne pas allouer headerHex/rawBuf pour un flow qui n'a encore vu qu'un SYN.

Fichiers : `internal/worker/dissect_l4.go`, `internal/worker/pipeline.go`, `internal/worker/worker.go`

> **Note de vérification :** Le coeur est exact : p.flows sans plafond, purgé uniquement par flushFlows (ticker 15 s, idle 20 s, worker.go:140-142) alors que le backlog requêtes a reqBacklogCap=1024 (pipeline.go:124). Deux corrections : (1) rawBuf n'est PAS alloué pour un flow SYN-only (captureRaw no-op sur payload vide, dissect_l4.go:72-74), cette partie de la proposition est déjà acquise ; headerHex en revanche est bien construit pour chaque paquet (extractL4Meta, worker.go:237-262, cap 128 octets hexdump). (2) Le filtre cBPF kernel restreint la surface : un nmap tous-ports sur un /16 est majoritairement éliminé en kernel (seuls tcp 80/8080/6379/5432/5672 passent) ; le scénario réaliste est un SYN flood/scan vers un port autorisé avec ports source aléatoires, qui crée bien des flowState non bornés. Le plafond maxFlows + compteur d'évictions reste pertinent.

#### CAP-7 - Couverture IPv6 incomplète : pas d'ICMPv6, offsets BPF statiques, endpoints eBPF IPv4-only

*impact moyen | effort M (1-3 j) | robustesse | ajusté*

Trois trous IPv6 distincts : (1) route() ne gère que layers.LayerTypeICMPv4, donc ping6/unreachable IPv6 n'apparaissent jamais ; (2) la branche 0x86dd du filtre cBPF lit les ports à offset fixe 0x36, donc tout paquet IPv6 avec extension headers (fragment, hop-by-hop) est éliminé, et icmp6 n'y figure pas du tout ; (3) decodeEvent (ebpf/loader.go) ne décode que saddr/daddr IPv4, donc les connexions TLS IPv6 gardent des endpoints synthétiques pid:<n> jamais enrichis par le hub. Pertinent pour les clusters dual-stack ou IPv6-only qui se généralisent. Traiter au minimum (1) et (3), (2) venant avec la génération runtime du filtre.

Fichiers : `internal/worker/worker.go`, `internal/worker/capture/afpacket_linux.go`, `internal/worker/ebpf/loader.go`, `internal/worker/ebpf/bpf`

> **Note de vérification :** Les trois constats sont exacts : (1) route() ne gère que LayerTypeICMPv4 (worker.go:218-221) ; (2) la branche 0x86dd lit les ports à l'offset fixe 0x36 (afpacket_linux.go:35) et ne laisse passer que next-header 0x06/0x11, jamais 58/ICMPv6 (lignes 34, 43) ; (3) decodeEvent ne décode que 4 octets saddr/daddr (ebpf/loader.go:59-64) et tls.bpf.c saute explicitement non-AF_INET (« IPv6 is skipped », bpf/tls.bpf.c:163,170). Correction d'approche : traiter (1) seul ne produit rien en capture live puisque ICMPv6 est éliminé en kernel avant route() ; (1) exige au minimum d'ajouter icmp6 au filtre cBPF statique (petit patch régénéré, pas besoin d'attendre la génération runtime complète du finding 1). (3) demande aussi de modifier la struct event côté C (adresses 16 octets + family), pas seulement le décodage Go.

#### CAP-8 - Capture sur 'any' en netns hôte : chaque paquet vu plusieurs fois fausse les métriques L4

*impact moyen | effort M (1-3 j) | perf | confirmé*

Le commentaire du l7Filter l'admet : sur un noeud CNI overlay, le même paquet passe par eth0 + l'interface vxlan + le veth du pod. Le réassembleur TCP absorbe les doublons, mais trackTCP compte chaque copie : bytes/packets doublés ou triplés, et surtout f.retransmits gonflé artificiellement (le doublon a un seq < nextSeq), rendant la métrique de retransmissions inutilisable pour diagnostiquer un vrai problème réseau. Pistes : supporter --iface multi-valeurs avec un ring par interface (en excluant les overlays par défaut), ou dédupliquer par (connKey, seq, len) dans une petite fenêtre, ou documenter un défaut par CNI dans le chart Helm.

Fichiers : `internal/worker/capture/afpacket_linux.go`, `internal/worker/worker.go`, `internal/worker/dissect_l4.go`, `internal/cli/worker.go`, `helm/k8shark/values.yaml`

### Dissecteurs L7

#### DIS-1 - Dissecteur HTTP/2 + gRPC (h2c et via eBPF TLS)

*impact fort | effort L (>3 j) | feature | confirmé*

Aujourd'hui un flux HTTP/2 est silencieusement perdu : consumeHTTPID parse la preface « PRI * HTTP/2.0 » comme une requête HTTP/1 puis échoue sur les frames binaires et abandonne la connexion. Or gRPC est le trafic est-ouest dominant en Kubernetes et c'est la demande n°1 sur ce type d'outil. Détecter la preface client (et le côté serveur via le premier frame SETTINGS), parser avec http2.Framer + hpack de golang.org/x/net (déjà en dépendance directe du go.mod, aucun module à ajouter), corréler HEADERS/DATA/trailers par stream ID (corrélation exacte, meilleure que le FIFO). Surfacer :method/:path/:status, et quand content-type=application/grpc émettre Protocol "grpc" avec service/méthode découpés du path et grpc-status/grpc-message des trailers, plus les champs de filtre grpc.method/grpc.status. Le chemin eBPF TLS (tls_pipeline.go) en bénéficie directement puisque le TLS déchiffré passe par le même dispatch, ce qui couvre les meshes et l'HTTPS interne.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/tls_pipeline.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `internal/hub/facets.go`, `ui/src/types.ts`

#### DIS-2 - Bug : les réponses aux requêtes HEAD désynchronisent tout le flux réponse

*impact fort | effort S (<1 j) | robustesse | confirmé*

consumeHTTPID parse le côté serveur avec http.ReadResponse(br, nil) : sans connaître la méthode, Go suppose GET et tente de lire Content-Length octets de body sur une réponse à HEAD qui n'en a pas. Le parser avale alors les octets des réponses suivantes comme body : sur une connexion keep-alive, toutes les paires requête/réponse suivantes sont fausses ou perdues (un healthcheck HEAD périodique suffit à casser la connexion). Correctif : le côté réponse consulte la méthode de la plus ancienne pendingReq de connState (déjà disponible sous p.mu) et passe un &http.Request{Method: ...} synthétique à ReadResponse. Ajouter un test à base d'octets réels HEAD + GET pipelinés dans dissect_test.go.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/dissect_test.go`

#### DIS-3 - Bug : les réponses intérimaires 1xx (Expect: 100-continue) décalent l'appariement FIFO

*impact fort | effort S (<1 j) | robustesse | confirmé*

http.ReadResponse retourne un « 100 Continue » ou « 103 Early Hints » comme une réponse complète ; completeResponse consomme alors la requête en attente, et la vraie réponse finale s'apparie avec la requête suivante : désynchronisation off-by-one permanente sur la connexion. C'est fréquent en pratique : libcurl envoie Expect: 100-continue sur tout POST > 1 Ko, et les 103 se généralisent. Correctif simple dans la boucle réponse : si 100 <= StatusCode <= 199 et != 101, drainer et continuer sans apparier (optionnellement noter l'intérimaire dans HTTPDetail). Le cas 101 est traité à part (voir finding WebSocket).

Fichiers : `internal/worker/pipeline.go`, `internal/worker/dissect_test.go`

#### DIS-4 - Bodies HTTP : décompression bornée (gzip/deflate) et rendu binaire-sûr partagé

*impact fort | effort S (<1 j) | ux | confirmé*

net/http ne décompresse jamais dans ReadResponse : un body Content-Encoding: gzip est stocké tel quel dans Payload.Body, donc illisible dans l'UI et invisible pour un filtre response.body contains. De même le body AMQP (string(pend.body)) et le body HTTP binaire partent bruts alors que Redis (redisDisplay) et Postgres (hex des params) savent déjà rendre le binaire proprement. Deux volets : (1) dans drainBody, si Content-Encoding est gzip/deflate, décompresser via la stdlib avec une limite stricte de sortie (garde anti zip-bomb, marquer Truncated) ; (2) factoriser un helper safeBody réutilisant isRedisPrintable qui remplace un body non imprimable par un aperçu hex + taille, appliqué aux bodies HTTP et AMQP. Gain quotidien immédiat : la majorité des APIs répondent compressé.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/dissect_amqp.go`, `internal/worker/dissect_redis.go`

#### DIS-5 - Sniff de contenu pour Redis/Postgres/AMQP sur ports non standard (plaintext)

*impact moyen | effort S (<1 j) | feature | confirmé*

consumeStreamID ne dispatch que par port bien connu (6379/5432/5672) : un Redis exposé sur 6380 ou un Postgres sur 5433 part dans le dissecteur HTTP qui échoue et jette le flux (il ne reste qu'un flow tcp générique). Le sniff de contenu existe pourtant déjà et il est testé : sniffTLS (tls_pipeline.go) reconnaît RESP, les messages Postgres typés/StartupMessage et l'en-tête AMQP à partir des premiers octets, avec inférence du rôle requête/réponse. Le réutiliser comme fallback dans la branche default de consumeStreamID avant le sniff HTTP (envelopper dans bufio.Reader, Peek, router). En k8s les remaps de ports sont courants et cela évite la config manuelle --redis-ports/--amqp-ports.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/tls_pipeline.go`, `internal/worker/dissect_test.go`

#### DIS-6 - Suivi des connexions WebSocket après le 101 Switching Protocols

*impact moyen | effort M (1-3 j) | feature | confirmé*

Après un upgrade réussi, la boucle réponse tente de parser les frames WebSocket comme des réponses HTTP, échoue et abandonne les deux directions : la connexion disparaît du dashboard (markL7 ayant même supprimé le flow L4 générique). Détecter le 101 + Upgrade: websocket côté réponse, puis basculer les deux directions sur un parser de frames RFC 6455 minimal (fin/opcode/mask/longueur, unmasking, aperçu borné des frames text, code de close) émettant des entries standalone comme le modèle push Redis. Ajouter Protocol "ws", un champ ws.opcode au filtre et une couleur UI. Très utile pour les apps temps réel et pour comprendre pourquoi une connexion « HTTP » reste ouverte des heures.

Fichiers : `internal/worker/pipeline.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `ui/src/types.ts`

#### DIS-7 - DNS sur TCP absent et clé d'appariement DNS trop faible

*impact moyen | effort M (1-3 j) | robustesse | confirmé*

Tout le TCP port 53 tombe dans le sniff HTTP et est perdu : or les resolvers rebasculent en TCP dès qu'une réponse UDP est tronquée (bit TC), et les gros enregistrements (SVCB/HTTPS, DNSSEC) rendent ce cas de plus en plus courant ; ce sont précisément les résolutions « bizarres » qu'on veut voir en debug. Ajouter dans consumeStreamID un cas port 53 : framing longueur 2 octets + décodage via layers.DNS.DecodeFromBytes, en réutilisant l'appariement de handleDNS. Au passage, renforcer dnsKey qui n'utilise que clientIP+ID : deux requêtes concurrentes du même IP (pods hostNetwork, SNAT) avec un ID en collision s'apparient mal ; inclure le port source client dans la clé.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/dissect_test.go`

#### DIS-8 - Dissecteur Kafka (corrélation native par correlation_id)

*impact moyen | effort L (>3 j) | feature | confirmé*

Kafka (port 9092, déjà listé dans wellKnownPorts donc visible seulement comme flow « tcp kafka ») est omniprésent dans les clusters event-driven. Le protocole s'y prête bien : chaque requête porte api_key/api_version/correlation_id et la réponse rappelle le correlation_id, ce qui permet un appariement exact par ID (pas de FIFO fragile) via une map correlation_id -> pendingReq par connexion. Scoper le MVP aux méthodes à forte valeur : Produce/Fetch (topics, partitions, tailles, error_code), Metadata, ApiVersions, avec un champ kafka.topic filtrable. Le coût vient du versionnement du protocole (flexible versions/tagged fields à partir de v9+) : viser un décodage best-effort des versions courantes et un skip propre au-delà.

Fichiers : `internal/worker/pipeline.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `internal/worker/demo.go`

#### DIS-9 - AMQP : extraire les basic properties du content header (correlation-id, reply-to, ...)

*impact moyen | effort M (1-3 j) | feature | confirmé*

Le frame header AMQP n'est lu que pour body-size (payload[4:12]) : le bitmask des property flags et la property list qui suivent (content-type, correlation-id, reply-to, message-id, delivery-mode, expiration, headers table) sont ignorés. correlation-id et reply-to sont pourtant l'outil n°1 pour suivre un RPC sur RabbitMQ, et content-type permettrait le rendu binaire-sûr du body. Parser le bitmask + les champs (les readers bornés amqpShortStr/amqpLongStr existent déjà ; la field table demande un petit décodeur récursif borné), remplir Payload.ContentType et de nouveaux champs CorrelationID/ReplyTo/MessageID dans pkg/api, exposer amqp.correlationid et amqp.replyto au filtre.

Fichiers : `internal/worker/dissect_amqp.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `internal/hub/facets.go`

#### DIS-10 - Résilience à la perte de segments TCP : LossErrors + resynchronisation

*impact moyen | effort M (1-3 j) | robustesse | confirmé*

tcpreader saute silencieusement les segments perdus (drops AF_PACKET, FlushOlderThan) : le dissecteur lit un flux avec un trou au milieu, misparse et, pire, l'appariement FIFO se décale sans détection possible (limitation documentée dans completeResponse mais rien ne la mitige). Activer tcpreader.ReaderStreamOptions{LossErrors: true} pour recevoir DataLost, et sur cet événement : purger les pendingReq de la connexion (mieux vaut perdre des paires que d'en émettre de fausses), puis resynchroniser sur la prochaine frontière de message (scan d'une ligne de requête/statut pour HTTP, type-byte + longueur plausible pour Postgres, marqueur RESP pour Redis). Exposer un compteur de resyncs dans WorkerStats pour rendre la dégradation visible à /api/workers.

Fichiers : `internal/worker/pipeline.go`, `internal/worker/worker.go`, `pkg/api/types.go`

#### DIS-11 - Dissecteurs MySQL et MongoDB

*impact moyen | effort L (>3 j) | feature | confirmé*

MySQL (3306) et MongoDB (27017) figurent déjà dans wellKnownPorts mais n'apparaissent que comme flows tcp génériques sans contenu. MySQL : protocole à paquets séquencés (longueur 3 octets + seq), surfacer COM_QUERY/COM_STMT_PREPARE/COM_STMT_EXECUTE côté requête et OK/ERR (code + message) / resultset (nombre de lignes) côté réponse, appariement FIFO comme Postgres ; attention au handshake et à la bascule TLS à détecter proprement. MongoDB : OP_MSG (opcode 2013) avec sections BSON, nécessite un mini-décodeur BSON borné pour extraire commande/collection ($db, find, insert...) et ok/errmsg de la réponse, appariement exact par requestID/responseTo. Réutiliser le modèle pgMaxPayload pour borner les allocations. Champs query/rowcount existants réutilisables, plus mongo.collection au filtre.

Fichiers : `internal/worker/pipeline.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `internal/worker/demo.go`

#### DIS-12 - Filtres IFL sur les en-têtes HTTP capturés

*impact moyen | effort S (<1 j) | dx | confirmé*

Les headers sont déjà capturés et normalisés en minuscules dans Payload.Headers, mais aucun champ de filtre ne permet de les interroger : impossible de filtrer sur user-agent, x-request-id, un header de trace (traceparent) ou un content-type de requête. Ajouter dans fieldGetter une résolution par préfixe request.header.<nom> / response.header.<nom> (compatible avec le contrat « champ inconnu = erreur de compilation » puisque le préfixe est reconnu explicitement), et alimenter l'autocomplete du front via facets.go avec les clés d'en-tête réellement observées dans le ring buffer. Petit effort, gros gain pour corréler avec le tracing existant (filtrer par x-request-id est un réflexe d'astreinte).

Fichiers : `internal/hub/filter.go`, `internal/hub/facets.go`, `ui/src/components/FilterBar.tsx`

### Hub (stockage, API, filtre IFL, enrichissement k8s)

#### HUB-1 - Persistance optionnelle du buffer et rétention configurable (durée et mémoire, pas seulement un compte fixe)

*impact fort | effort L (>3 j) | robustesse | confirmé*

Le store est un ring buffer en mémoire de 10000 entrées (internal/config/config.go, EntryBufferSize) : tout l'historique est perdu à chaque restart/OOM du hub, et sous fort trafic la fenêtre utile peut se réduire à quelques secondes alors que /api/timeline promet 15 minutes par défaut. Proposer un backend de persistance optionnel activé par flag (--store-dir) : segments append-only JSONL (ou SQLite embarqué via modernc.org/sqlite, sans cgo) avec rotation par taille/âge, rechargement des N dernières entrées au démarrage pour repeupler le ring et les facets. En complément, ajouter une rétention par durée (--retention 30m) et une borne mémoire approximative (taille cumulée des Payload) en plus du compte d'entrées, exposées dans values.yaml. Le ring reste le chemin chaud ; la persistance ne sert que le rechargement et les requêtes historiques hors buffer.

Fichiers : `internal/hub/store.go`, `internal/hub/server.go`, `internal/config/config.go`, `internal/cli/hub.go`, `helm/k8shark/values.yaml`

#### HUB-2 - IFL : opérateurs regex (matches), liste (in) et startswith

*impact fort | effort M (1-3 j) | feature | confirmé*

Le langage a déjà and/or/not, parenthèses, contains et les comparaisons numériques, mais il manque les trois opérateurs les plus demandés en pratique : request.path matches "^/api/v[0-9]+/", dst.namespace in ("prod", "staging"), request.host startswith "api.". Le lexer/parser récursif de filter.go s'y prête bien : compiler la regex une seule fois dans CompileFilter (regexp.MustCompilePOSIX inutile, RE2 de Go est déjà sans backtracking catastrophique, borner juste la taille du motif), et parser une liste parenthésée de littéraux après "in". Mettre à jour compare(), les tableaux d'opérateurs de facets.go (opsString/opsText) et l'autocomplete du FilterBar pour rester synchrone avec /api/fields, comme l'exige CLAUDE.md. Gros gain d'expressivité pour l'UI et le MCP sans toucher au modèle de données.

Fichiers : `internal/hub/filter.go`, `internal/hub/facets.go`, `internal/hub/filter_test.go`, `ui/src/components/FilterBar.tsx`

#### HUB-3 - Pagination robuste par numéro de séquence au lieu de before=<id>

*impact moyen | effort S (<1 j) | robustesse | ajusté*

recentBefore (store.go) retourne une liste vide dès que l'entrée ancre a été évincée du ring, ce qui arrive en quelques secondes sous fort trafic (buffer 10k) : le "load older" de l'UI et du MCP se casse silencieusement. Attribuer un numéro de séquence monotone hub-side à chaque add() (champ additif seq dans api.Entry, json omitempty, nil-safe conformément au contrat pkg/api), puis paginer avec ?before_seq=N : le walk-back saute simplement les entrées de seq >= N, sans dépendre de la présence de l'ancre. Retourner aussi un nextCursor et un booléen hasMore dans la réponse pour que le client sache s'arrêter. Petit changement, supprime une classe entière de pagination cassée.

Fichiers : `internal/hub/store.go`, `internal/hub/server.go`, `pkg/api/types.go`, `ui/src/useHub.ts`

> **Note de vérification :** Exact pour l'UI : recentBefore retourne une liste vide si l'ancre a été évincée (internal/hub/store.go:119-148, le commentaire l'assume) et ui/src/useHub.ts:270 pagine avec before=oldest.id. Correction : le MCP n'est PAS affecté, list_entries n'a aucun paramètre before (internal/mcp/server.go:275-291 : filter/limit/since/until seulement). Autre point : /api/entries renvoie aujourd'hui un tableau nu (server.go:596-599), ajouter nextCursor/hasMore change la forme de réponse et demande une adaptation UI.

#### HUB-4 - Fan-out WebSocket : batching des entrées et cache des marshals pour le replay

*impact moyen | effort M (1-3 j) | perf | confirmé*

broadcast() marshale une seule fois par entrée (bien) mais fait un WriteMessage par client et par entrée : à 2000 entrées/s avec 10 clients, cela fait 20000 frames/s et autant de syscalls, plus une prise de RLock par entrée. Introduire un type MsgEntryBatch dans l'Envelope et accumuler les entrées 25 à 50 ms avant fan-out (le statsLoop montre déjà le pattern ticker), ce qui divise les frames par un facteur 50 à 100 sous charge. Par ailleurs replayHistory re-marshale jusqu'à 500 entrées à chaque connexion et à chaque changement de filtre de chaque client : stocker les bytes pré-marshalés à côté de l'*api.Entry dans le ring (le JSON d'une entrée est immuable après add) rend le replay quasi gratuit. Mesurable directement avec le compteur broadcastDropped existant.

Fichiers : `internal/hub/server.go`, `internal/hub/store.go`, `pkg/api/types.go`, `ui/src/useHub.ts`

#### HUB-5 - Métriques Prometheus manquantes : protocole, statut, remplissage du buffer, débit, santé de l'enrichissement

*impact moyen | effort S (<1 j) | ops | confirmé*

handleMetrics n'expose que 4 séries hub (entries_total, front_clients, workers, broadcast_dropped) alors que le store possède déjà byProtocol et byStatus, jamais exportés : impossible d'alerter sur un taux d'erreurs HTTP ou une chute du trafic DNS sans passer par l'API JSON. Ajouter k8shark_hub_entries_by_protocol_total{protocol=...}, k8shark_hub_entries_by_status_total{status=...}, k8shark_hub_buffer_entries et k8shark_hub_buffer_capacity (le remplissage indique la profondeur d'historique réelle), k8shark_hub_entries_per_sec (déjà calculé dans stats()), plus un compteur d'échecs de refresh k8s côté resolver pour rendre visible un RBAC cassé. Tout est déjà en mémoire, c'est de l'exposition pure dans le format texte hand-rolled existant.

Fichiers : `internal/hub/server.go`, `internal/hub/store.go`, `internal/hub/k8s.go`

#### HUB-6 - Enrichissement k8s : watch incrémental au lieu du re-list complet toutes les 20 s, et rattrapage des entrées non résolues

*impact moyen | effort M (1-3 j) | feature | confirmé*

refresh() re-liste tous les pods, replicasets et services du cluster toutes les 20 s (k8s.go), ce qui charge inutilement l'API server sur un gros cluster et laisse jusqu'à 20 s de fenêtre où un pod fraîchement créé n'est pas résolu ; comme enrich() ne s'applique qu'à l'ingestion, ces entrées gardent une IP nue pour toujours dans le buffer. Passer sur l'API watch (GET ...?watch=true&resourceVersion=N, toujours en stdlib comme le style actuel du resolver) avec re-list de resynchronisation en secours, pour une latence de résolution quasi nulle. Pour le rattrapage, garder une petite liste des IDs d'entrées avec IP non résolue et les ré-enrichir au refresh suivant, en veillant au partage de pointeurs (enrichir avant broadcast, ou copier l'entrée avant mutation). Bonus facile au passage : capturer quelques labels de pod (app, version) pour de futurs champs de filtre.

Fichiers : `internal/hub/k8s.go`, `internal/hub/k8s_test.go`

#### HUB-7 - Tri côté serveur sur /api/entries (?sort=elapsedMs&order=desc)

*impact moyen | effort S (<1 j) | feature | confirmé*

L'API ne sait retourner que du newest-first : répondre à "les 20 requêtes les plus lentes" ou "les plus grosses réponses" oblige l'UI ou le MCP à rapatrier tout le buffer et trier côté client. Ajouter ?sort=<champ IFL numérique>&order= sur handleEntries : un tas borné à limit (container/heap) pendant le walk du ring donne le top-N en O(n log limit) sans copie complète, en réutilisant fieldGetter pour l'extraction et en rejetant les champs non numériques comme le fait déjà validGroupBy. Exposer ensuite le paramètre dans l'outil MCP list_entries, où c'est immédiatement exploitable par un agent ("trouve les requêtes lentes vers postgres").

Fichiers : `internal/hub/server.go`, `internal/hub/store.go`, `internal/mcp/server.go`

#### HUB-8 - Garde-fou multi-réplicas : le chart accepte hub.replicas > 1 alors que tout l'état est local au pod

*impact moyen | effort S (<1 j) | ops | ajusté*

values.yaml expose hub.replicas et le Deployment le consomme tel quel, mais le ring buffer, le registre workers, les facets et les connexions WebSocket sont purement en mémoire du pod : avec 2 réplicas derrière le Service, chaque worker n'alimente qu'un hub, chaque client front n'en voit qu'un, et POST /api/workers/capture ne touche que les workers de son réplica ; l'utilisateur voit un trafic aléatoirement amputé sans erreur. Court terme (le S proposé) : fail(...) dans le template Helm si replicas > 1 avec un message expliquant la limite, et une note dans values.yaml et le README ; c'est le comportement honnête tant qu'il n'y a pas de partage d'état. Moyen terme, documenter la piste sharding (fan-out des commandes via tous les réplicas et agrégation côté front) sans la construire maintenant.

Fichiers : `helm/k8shark/templates/hub.yaml`, `helm/k8shark/values.yaml`, `README.md`

> **Note de vérification :** Le constat technique est exact : hub.yaml:62 consomme .Values.hub.replicas sans aucun fail() (grep 'fail' dans helm/k8shark/templates : zéro résultat) et tout l'état est en mémoire du pod. Correction : la note README demandée existe déjà (README.md:128 : 'Hub replica count (state is per-pod; there is no shared backing store)'). Reste pertinent : le fail() ou warning Helm et le commentaire dans values.yaml (rien à côté de replicas: 1, values.yaml:27).

#### HUB-9 - Purge du registre workers : les nœuds disparus s'accumulent sans limite

*impact faible | effort S (<1 j) | robustesse | confirmé*

workerUpdate crée une ligne par nœud jamais supprimée : sur un cluster avec autoscaling ou nœuds spot, /api/workers et les séries Prometheus par worker (k8shark_worker_*{node=...}) grossissent indéfiniment avec des lignes Connected=false obsolètes, ce qui pollue l'UI workers en cours et fait de la cardinalité de métriques fantôme. Ajouter un GC simple dans statsLoop (déjà un ticker 2 s) : supprimer les entrées déconnectées dont LastSeen dépasse un TTL (par exemple 1 h, configurable), en gardant la fenêtre courte volontairement documentée pour le cas incident ("le worker était là il y a 2 min") que le commentaire de workerInfo décrit. Une dizaine de lignes plus un test.

Fichiers : `internal/hub/server.go`, `internal/hub/server_test.go`

#### HUB-10 - facets.observe : précalculer les getters au lieu de résoudre fieldGetter à chaque entrée

*impact faible | effort S (<1 j) | perf | confirmé*

observe() (facets.go) appelle fieldGetter(name) pour chacun des ~45 champs trackés, pour chaque entrée ingérée, sous le mutex du facetIndex : chaque appel re-traverse le grand switch de filter.go et alloue une closure, soit ~45 allocations et résolutions par entrée sur le chemin chaud d'ingestion. Résoudre les getters une seule fois dans newFacetIndex (stocker le func à côté du fieldCounter) supprime ce coût ; le garde-fou existant "catalog/getter drift" se déplace simplement à la construction. Gain modeste mais gratuit sur un chemin exécuté pour chaque entrée du cluster, et le TestFieldCatalogMatchesGetter existant couvre déjà la cohérence.

Fichiers : `internal/hub/facets.go`, `internal/hub/filter.go`

### Front React (UX et code)

#### UI-1 - Ancrage du scroll pendant le streaming (lecture sans gel manuel)

*impact fort | effort M (1-3 j) | ux | confirmé*

Les nouvelles entrées sont préfixées en tête de liste (scheduleFlush dans useHub.ts fait buf.reverse().concat(prev)) : dès que l'utilisateur descend dans la table pour lire, chaque flush décale toutes les lignes vers le bas et la ligne sous ses yeux fuit en continu ; il doit penser à cliquer Pause. Comme la hauteur de ligne est constante (ROW_HEIGHT = 29), la correction est simple : dans un useLayoutEffect de TrafficTable, si scrollTop > 0 et tri inactif, compter les entrées préfixées (index de l'ancien premier id dans le nouveau tableau) et compenser scrollRef.scrollTop += k * ROW_HEIGHT. Ajouter en complément une pastille flottante « N nouvelles entrées, revenir en haut » façon Slack/DevTools. C'est le plus gros irritant de l'expérience temps réel actuelle.

Fichiers : `ui/src/components/TrafficTable.tsx`, `ui/src/useHub.ts`, `ui/src/styles.css`

#### UI-2 - Timeline/histogramme cliquable exploitant /api/timeline (déjà côté hub)

*impact fort | effort M (1-3 j) | feature | confirmé*

Le hub expose déjà GET /api/timeline (buckets avec entries/errors/warnings) et /api/entries accepte ?since=/?until=, mais le front n'utilise aucun des deux : la seule vue temporelle est une sparkline de 90 px non interactive. Ajouter une bande histogramme sous la FilterBar (SVG maison, cohérent avec l'approche sans lib de ServiceMap/Sparkline) : barres empilées ok/warning/error, brush à la souris pour sélectionner une plage qui charge /api/entries?since&until&filter dans la table (flux live mis en pause, bouton « retour au live »). Cela transforme l'outil de « ce qui passe maintenant » en « ce qui s'est passé pendant l'incident il y a 10 minutes », le cas d'usage SRE principal.

Fichiers : `ui/src/App.tsx`, `ui/src/useHub.ts`, `ui/src/components/FilterBar.tsx`, `ui/src/types.ts`, `ui/src/styles.css`

#### UI-3 - Copier en cURL (et base pour rejouer une requête HTTP)

*impact moyen | effort S (<1 j) | feature | confirmé*

EntryDetail affiche méthode, path, host, headers et body des requêtes HTTP mais n'offre aucun moyen de les rejouer : l'utilisateur retape tout à la main. Un bouton « copier en cURL » dans l'en-tête du détail (génération purement client : method + host + path + query + headers + --data pour le body, en excluant les headers hop-by-hop) couvre 90 % du besoin sans toucher au hub. Une vraie fonction « replay » (POST vers un endpoint hub qui ré-émet la requête dans le cluster) peut venir ensuite, mais le copier-cURL est un quick win à très bon ratio valeur/effort.

Fichiers : `ui/src/components/EntryDetail.tsx`

#### UI-4 - Navigation clavier ↑/↓ dans la table des entrées

*impact moyen | effort S (<1 j) | ux | ajusté*

Les lignes sont focusables (tabIndex=0, Enter/Espace sélectionne) mais passer d'une entrée à la suivante exige un clic ou un Tab par ligne : il n'y a pas de parcours flèches haut/bas (ou j/k) comme dans Wireshark ou les DevTools. Ajouter dans App.tsx (le handler keydown global existe déjà) ArrowUp/ArrowDown quand rien ne capte la frappe : déplacer la sélection dans displayEntries et appeler rowVirtualizer.scrollToIndex pour garder la ligne visible. Le panneau détail suit automatiquement puisque selectedLive est déjà resynchronisé. Triage d'un flux d'erreurs beaucoup plus rapide.

Fichiers : `ui/src/App.tsx`, `ui/src/components/TrafficTable.tsx`

> **Note de vérification :** Constat exact : le keydown global (App.tsx:106-120) ne gère que /, espace et Escape, et les lignes (tabIndex=0, TrafficTable.tsx:327) n'ont pas de parcours flèches. Correction d'approche : displayEntries (ordre trié, TrafficTable.tsx:163-168) et rowVirtualizer (l.177) sont locaux à TrafficTable, pas visibles depuis App.tsx ; le handler doit vivre dans TrafficTable (ou exposer scrollToIndex/ordre via ref ou state remonté), sinon la sélection ignorerait le tri actif.

#### UI-5 - Endpoints cliquables dans le détail : « suivre ce flux » et filtrer par service

*impact moyen | effort S (<1 j) | ux | confirmé*

Les cartes source/destination d'EntryDetail (EndpointCard) sont inertes alors que la donnée pour pivoter est là : ajouter des actions « filtrer sur cette source », « sur cette destination » et « suivre cette conversation » qui appliquent un clause IFL (src.ip == ... and dst.ip == ..., ou src.name/dst.name quand l'enrichissement k8s a résolu le nom), équivalent du Follow TCP Stream de Wireshark. Il suffit de passer onApply depuis App.tsx à EntryDetail et de générer la clause comme le fait déjà nodeClause dans ServiceMap.tsx. Pivoter d'une entrée vers tout le trafic de la même paire est un réflexe d'investigation constant.

Fichiers : `ui/src/components/EntryDetail.tsx`, `ui/src/App.tsx`

#### UI-6 - Historique des filtres récents dans l'autocomplete

*impact moyen | effort S (<1 j) | ux | confirmé*

Les exemples de la FilterBar sont statiques (EXAMPLES codé en dur) et un filtre IFL composé doit être retapé à chaque session : aucun historique n'est conservé. Persister les filtres appliqués avec succès dans localStorage (dédupliqués, plafonnés à ~10, comme le fait déjà VISIBLE_COLUMNS_KEY pour les colonnes), et les proposer en tête du dropdown FilterSuggest quand l'input est vide, avec ArrowUp pour rappeler le dernier. Réduit fortement la friction du langage de filtre, qui est le cœur de l'outil.

Fichiers : `ui/src/components/FilterBar.tsx`, `ui/src/components/FilterSuggest.tsx`

#### UI-7 - Vue « Top » (top talkers) exploitant /api/summary (déjà côté hub)

*impact moyen | effort M (1-3 j) | feature | confirmé*

Le hub calcule déjà GET /api/summary?groupBy=workload|namespace|<champ IFL> avec count, erreurs, p50/p95/max et protocoles par groupe (summary.go), mais seul le MCP le consomme : le front n'a aucune vue agrégée chiffrée. Ajouter un troisième onglet au view-switch (List | Map | Top) : table triable des workloads/namespaces avec appels, taux d'erreur, latences p50/p95, clic sur une ligne pour appliquer le filtre correspondant et revenir à la liste (même pattern que onNodeClick de ServiceMap). Donne la vue d'ensemble « qui parle le plus / qui est en erreur » qui manque entre la table brute et la map.

Fichiers : `ui/src/App.tsx`, `ui/src/components/FilterBar.tsx`, `ui/src/types.ts`, `ui/src/styles.css`

#### UI-8 - Tenue de charge à 10k+ entrées : tri recalculé à chaque frame et recherches O(n)

*impact moyen | effort M (1-3 j) | perf | confirmé*

La virtualisation absorbe bien le rendu, mais deux coûts croissent avec le buffer : quand un tri de colonne est actif, displayEntries copie et re-trie tout le tableau à chaque flush rAF (O(n log n) par frame, n non borné après des « load older » puisque capRef monte sans limite), et selectedLive refait un entries.find O(n) à chaque rendu. Correctifs : geler le flux quand un tri est actif (bannière « tri actif, flux figé, N nouvelles ») ou insérer les nouvelles entrées par dichotomie ; indexer les entrées par id dans une Map pour selectedLive ; exposer la taille du buffer (500/2000/10000) dans l'UI au lieu du MAX_ENTRIES fixe. Rend le comportement prévisible sur les clusters à fort trafic.

Fichiers : `ui/src/components/TrafficTable.tsx`, `ui/src/useHub.ts`, `ui/src/App.tsx`

#### UI-9 - Chips de statut cliquables dans le header, comme les pilules protocole

*impact moyen | effort S (<1 j) | ux | confirmé*

Les pilules protocole du StatsHeader togglent une clause protocol == x dans le filtre, mais les chips success/warning/error juste à côté sont des <span> inertes alors que status est un champ IFL valide (filter.go, case "status"). Les transformer en boutons qui togglent status == error (même mécanique add/swap/remove que toggleProtoFilter dans App.tsx, généralisable au champ status). « Montre-moi seulement les erreurs » en un clic est probablement l'interaction la plus demandée d'un tel dashboard.

Fichiers : `ui/src/components/StatsHeader.tsx`, `ui/src/App.tsx`

#### UI-10 - Régions aria-live pour les erreurs de filtre et l'état de connexion

*impact faible | effort S (<1 j) | ux | confirmé*

L'app a une bonne base a11y (rôles combobox/tablist, aria-sort, focus-visible) mais aucun aria-live : un filtre invalide (.filter-error), le passage en « reconnecting… » ou la confirmation de copie sont purement visuels et invisibles aux lecteurs d'écran. Ajouter role="alert" sur le message d'erreur de filtre, aria-live="polite" sur l'indicateur de connexion et sur le compteur « N shown », et un texte sr-only pour l'état copié du CopyButton. Effort minime, conforme aux attentes WCAG pour du contenu dynamique.

Fichiers : `ui/src/components/FilterBar.tsx`, `ui/src/components/StatsHeader.tsx`, `ui/src/components/EntryDetail.tsx`

#### UI-11 - Panneau détail redimensionnable (largeur fixe 440 px)

*impact faible | effort S (<1 j) | ux | confirmé*

Le panneau EntryDetail est figé à width: 440px (styles.css) : les requêtes Postgres longues, les gros bodies JSON et les tables DNS y sont à l'étroit, alors que la moitié de l'écran est occupée par la table qu'on ne lit plus une fois une entrée ouverte. Ajouter une poignée de redimensionnement sur la bordure gauche (div avec pointer events, largeur persistée dans localStorage comme les colonnes) avec un double-clic pour revenir à la valeur par défaut. Amélioration de confort simple et sans dépendance.

Fichiers : `ui/src/components/EntryDetail.tsx`, `ui/src/styles.css`, `ui/src/App.tsx`

### Serveur MCP

#### MCP-1 - Compléter start_pcap : synthèse d'un fichier PCAP hub-side depuis le buffer

*impact fort | effort M (1-3 j) | feature | confirmé*

Le handler handleStartPcap (internal/mcp/server.go:559) renvoie un texte « not yet available » alors que l'outil est déjà annoncé derrière --allow-capture. La logique de synthèse de paquets depuis des entries existe déjà côté client (ui/src/pcap.ts, en cours) ; la porter en Go derrière un endpoint hub GET /api/pcap?filter=&since=&until= qui rejoue le ring buffer en paquets TCP/UDP synthétiques, puis faire écrire le résultat par le MCP dans un fichier local dont le chemin est retourné à l'agent. Un agent IA de debug peut alors ouvrir la capture dans tshark/Wireshark, ce qui est exactement le cas d'usage promis par le nom de l'outil. Renommer au passage l'outil en export_pcap pour refléter la sémantique réelle (export du buffer, pas capture live).

Fichiers : `internal/mcp/server.go`, `internal/hub/server.go`, `internal/cli/mcp.go`

#### MCP-2 - Outil diff_traffic : comparaison de deux fenêtres temporelles

*impact fort | effort S (<1 j) | feature | confirmé*

Question numéro un d'un agent de debug : « qu'est-ce qui a changé depuis l'incident ? ». Ajouter un outil diff_traffic(baseline_since/until, current_since/until, group_by, filter) qui appelle deux fois /api/summary (l'endpoint accepte déjà since/until et groupBy) et calcule par groupe les deltas de volume, de taux d'erreur et de p95, triés par régression la plus forte, en signalant les groupes apparus/disparus. Implémentable entièrement côté MCP sans toucher au hub, en réutilisant handleTrafficSummary. Réduit énormément le nombre d'allers-retours de l'agent, qui aujourd'hui doit faire deux get_traffic_summary et diffuser lui-même.

Fichiers : `internal/mcp/server.go`, `internal/hub/summary.go`

#### MCP-3 - Outil find_error_clusters : erreurs groupées par signature

*impact fort | effort M (1-3 j) | feature | confirmé*

Aujourd'hui l'agent doit lister les entries en erreur une à une (list_entries filter="status == error") puis les regrouper mentalement. Ajouter un outil qui récupère les entries en erreur/warning sur la fenêtre demandée et les agrège par signature (protocol, dst.workload, statusCode, résumé de réponse normalisé : chiffres et IDs remplacés par des jokers), en retournant par cluster le compte, first/last seen, et 2-3 IDs d'entries exemples à passer à get_entry. C'est la réponse directe à « quelles familles d'erreurs y a-t-il en ce moment ? » et le point d'entrée naturel d'une session de debug. Faisable côté MCP en s'appuyant sur /api/entries.

Fichiers : `internal/mcp/server.go`

#### MCP-4 - Outil get_service_graph : dépendances et remontée de chaîne d'appels

*impact moyen | effort M (1-3 j) | feature | confirmé*

Le front a une service map mais rien n'expose le graphe aux agents : validGroupBy (internal/hub/summary.go) ne permet qu'un seul champ, donc impossible d'agréger par paire src→dst via /api/summary. Ajouter un endpoint hub /api/graph (arêtes src.workload→dst.workload avec compte, erreurs, p50/p95) et un outil MCP get_service_graph avec paramètres filter/since/until et un paramètre optionnel focus=namespace/workload qui restreint aux arêtes entrantes/sortantes du workload. L'agent peut alors résoudre une chaîne d'appels (« qui appelle le service en 500, et qui celui-ci appelle-t-il ? ») en un ou deux appels au lieu de tirer des centaines d'entries brutes.

Fichiers : `internal/mcp/server.go`, `internal/hub/summary.go`, `internal/hub/server.go`

#### MCP-5 - Pagination par curseur et plafond de taille des réponses d'outils

*impact moyen | effort S (<1 j) | dx | confirmé*

Le hub supporte déjà la pagination par curseur (?before=, store.recentBefore, internal/hub/server.go:595) mais list_entries ne l'expose pas : au-delà de limit=1000 l'agent est aveugle sur le reste du buffer. Exposer un argument before dans list_entries et terminer la sortie par un hint « next cursor: <dernier id> » quand la page est pleine. Ajouter aussi un plafond global en octets sur le texte retourné (list_entries à 1000 entrées ou get_entry avec un gros body Payload.Body peut dépasser la fenêtre de contexte du client), avec troncature explicite indiquant comment affiner (filter, limit, before).

Fichiers : `internal/mcp/server.go`, `internal/hub/server.go`, `internal/hub/store.go`

#### MCP-6 - Conformité JSON-RPC, traitement concurrent des appels, et tests du package mcp

*impact moyen | effort M (1-3 j) | robustesse | confirmé*

Trois faiblesses protocole vérifiées dans server.go : (1) une ligne JSON malformée est ignorée sans réponse (handleLine:146) alors que JSON-RPC exige une erreur -32700 avec id null, un client peut donc rester bloqué en attente ; (2) toute la boucle est séquentielle, un tools/call lent (timeout HTTP hub à 10 s) bloque les ping et les autres appels, il faut traiter chaque requête dans une goroutine avec un mutex d'écriture sur stdout ; (3) internal/mcp n'a aucun test. Ajouter server_test.go avec un hub httptest.Server factice couvrant initialize, tools/list, un tools/call heureux, les erreurs (outil inconnu, hub down, 401) et les cas malformés.

Fichiers : `internal/mcp/server.go`

#### MCP-7 - Moderniser initialize : négociation de version, champ instructions, annotations readOnlyHint

*impact moyen | effort S (<1 j) | dx | ajusté*

Le serveur répond toujours protocolVersion "2024-11-05" sans lire la version demandée par le client (dispatch:164), alors que la spec demande d'écho la version cliente si supportée ; les révisions 2025 apportent en plus les annotations d'outils. Trois ajouts peu coûteux : négocier la version (renvoyer celle du client si connue, sinon la nôtre), remplir le champ instructions du résultat initialize avec le workflow recommandé (get_stats/get_traffic_summary d'abord, list_entries pour approfondir, get_entry pour le détail, list_filter_fields avant tout filtre complexe), et annoter chaque outil avec annotations.readOnlyHint=true (sauf start_pcap) pour que les clients MCP puissent auto-approuver les appels sans prompt. Amélioration directe de l'autonomie des agents.

Fichiers : `internal/mcp/server.go`

> **Note de vérification :** Faits vérifiés : protocolVersion figé à 2024-11-05 (server.go:26), le handler initialize (server.go:162-169) ignore totalement req.Params, pas de champ instructions, toolDef sans annotations (server.go:85-89). Correction de cadrage : répondre systématiquement 2024-11-05 est techniquement conforme à la spec (le serveur ne supporte qu'une version, et la spec autorise à répondre une version supportée si celle du client ne l'est pas). Le vrai travail n'est pas d'écho la version mais de supporter la révision 2025-03-26+, prérequis pour que les annotations readOnlyHint soient comprises des clients ; instructions, lui, existe déjà dans la révision 2024-11-05 et peut être ajouté immédiatement.

#### MCP-8 - Documentation d'installation du serveur MCP dans les clients

*impact moyen | effort S (<1 j) | dx | confirmé*

Le README ne consacre qu'une ligne de tableau au MCP : aucune instruction pour l'enregistrer dans un client. Ajouter une section README (ou docs/mcp.md) avec les snippets concrets : claude mcp add k8shark -- k8shark mcp --hub http://localhost:8898, le bloc JSON équivalent pour Claude Desktop/Cursor (.mcp.json), le prérequis k8shark proxy ou tap pour avoir le hub joignable, et la variable K8SHARK_API_TOKEN quand le hub est authentifié. Éventuellement une sous-commande k8shark mcp --print-config qui imprime le bloc JSON prêt à coller. Sans cela, la fonctionnalité la plus différenciante du projet reste invisible pour les utilisateurs.

Fichiers : `README.md`, `internal/cli/mcp.go`

### Déploiement et ops (Helm, images, CI/CD)

#### OPS-1 - Stamping de version cassé : mauvais chemin de module dans -ldflags et VERSION jamais passé en CI

*impact fort | effort S (<1 j) | ops | confirmé*

Le Makefile (ligne 13) et build/k8shark.Dockerfile (ligne 24) injectent la version via -X github.com/coe/k8shark/internal/config.Version alors que le module s'appelle github.com/pablocolson/k8shark : le -X est silencieusement ignoré et `k8shark version` répond toujours "dev". En plus, la CI ne passe aucun build-arg VERSION aux étapes docker/build-push-action, donc même corrigé, les images taguées vX.Y.Z rapporteraient "dev". Corriger le chemin aux deux endroits et ajouter `build-args: VERSION=${{ github.ref_name }}` (ou la sortie de metadata-action) dans le job docker. Impact exploitant direct : impossible aujourd'hui de savoir quelle version tourne dans le cluster.

Fichiers : `Makefile`, `build/k8shark.Dockerfile`, `.github/workflows/ci.yml`, `internal/config/config.go`

#### OPS-2 - Images CI en linux/amd64 uniquement : pas de support arm64

*impact fort | effort M (1-3 j) | ops | confirmé*

Le job docker de ci.yml build/push avec platforms: linux/amd64 seul, alors que le Makefile a déjà une cible docker-buildx amd64+arm64 (jamais utilisée en CI). Un DaemonSet worker sur des noeuds arm64 (Graviton, Ampere, Raspberry Pi, k3s sur Apple Silicon) tombe en exec format error. Le binaire étant cgo (AF_PACKET), le cross-build QEMU serait lent : utiliser le runner natif ubuntu-24.04-arm (gratuit pour les repos publics) dans une matrix + un job manifest merge, ou cross-compiler avec CC=aarch64-linux-gnu-gcc. L'image front (node+nginx) passe en multi-arch trivialement.

Fichiers : `.github/workflows/ci.yml`, `build/k8shark.Dockerfile`, `build/front.Dockerfile`, `Makefile`

#### OPS-3 - Aucune release automatisée : ni binaires CLI, ni publication du chart Helm

*impact fort | effort M (1-3 j) | ops | confirmé*

Le point d'entrée du produit est `k8shark tap`, qui exige le binaire CLI en local, mais il n'existe aucune GitHub Release avec binaires précompilés (darwin/linux, amd64/arm64) ni publication du chart (le chart n'est poussé ni en OCI sur ghcr ni via chart-releaser ; Chart.yaml reste figé à version/appVersion 0.1.0). Un utilisateur doit cloner et compiler (avec les pièges macOS LC_UUID). Ajouter un workflow release sur tag : goreleaser (le -linkmode=external côté darwin est déjà géré dans le Makefile, à reporter dans la config), `helm push` vers oci://ghcr.io/pablocolson/charts avec bump automatique de version/appVersion depuis le tag, et notes de release.

Fichiers : `.github/workflows/ci.yml`, `helm/k8shark/Chart.yaml`, `Makefile`

#### OPS-4 - k8shark clean supprime le namespace entier sans garde-fou

*impact fort | effort S (<1 j) | ux | ajusté*

k8s.Uninstall (internal/k8s/deploy.go, lignes 107-110) enchaîne helm uninstall puis `kubectl delete namespace` inconditionnel. Si l'utilisateur a installé via `tap -n monitoring` dans un namespace partagé, `clean -n monitoring` détruit tout le namespace et ses autres workloads : footgun de classe perte de données. Correction simple : ne supprimer le namespace que s'il porte le label app.kubernetes.io/part-of=k8shark posé par ensureNamespace (vérification kubectl get ns -o jsonpath), plus un flag --keep-namespace et une confirmation interactive si d'autres pods non-k8shark y tournent.

Fichiers : `internal/k8s/deploy.go`, `internal/cli/clean.go`, `internal/cli/tap.go`

> **Note de vérification :** Footgun réel : deploy.go:107-110 fait kubectl delete namespace inconditionnel et clean.go n'a ni flag ni confirmation. Mais le garde-fou proposé (label app.kubernetes.io/part-of=k8shark) ne protège PAS le scénario décrit : ensureNamespace (deploy.go:74-77) kubectl-apply le manifest labellisé même sur un namespace préexistant, donc `tap -n monitoring` pose le label sur le namespace partagé. Correction : garder --keep-namespace + détection de pods/ressources non-k8shark avant suppression (ou mémoriser si tap a créé le ns), le label seul ne discrimine rien.

#### OPS-5 - Aucune NetworkPolicy : le hub et ses données L7 sont accessibles à tout pod du cluster

*impact fort | effort M (1-3 j) | securite | confirmé*

Le hub concentre le trafic capturé de tout le cluster (bodies non redigés, requêtes Postgres/Redis) et son Service est joignable par n'importe quel pod ; sans hub.apiToken (défaut vide) l'API est totalement ouverte, et même avec token celui-ci transite en ws:// clair. Ajouter au chart une NetworkPolicy optionnelle (networkPolicy.enabled) : ingress du hub restreint aux pods k8shark-front et aux workers (hostNetwork : autoriser par ipBlock/namespace selon CNI, à documenter), ingress du front restreint au contrôleur d'ingress, et exposer /metrics au scraper via un port/selector dédié. Le README deploy/ cible déjà Cilium, donc fournir aussi une variante CiliumNetworkPolicy en commentaire serait cohérent.

Fichiers : `helm/k8shark/templates/`, `helm/k8shark/values.yaml`, `deploy/k8shark.yaml`

#### OPS-6 - Dérive du manifest statique deploy/ : worker sur-privilégié en permanence et RBAC plus large que le chart

*impact moyen | effort M (1-3 j) | securite | ajusté*

deploy/k8shark.yaml se présente comme rendu du chart canonique mais a dérivé : le worker y est toujours privileged + hostPID avec les 7 capabilities eBPF et des monts hostPath debugfs/cgroup en écriture + bpffs en mountPropagation Bidirectional, alors que le chart gate tout cela sur worker.tls.enabled ; le ClusterRole y ajoute watch, nodes, namespaces, endpointslices, deployments, statefulsets que internal/hub/k8s.go n'utilise pas (le chart se limite à get/list sur pods/services/replicasets). Les utilisateurs kubectl-apply héritent donc d'une surface de privilèges inutile. Générer ce fichier via `helm template` (cible make deploy-manifest) et ajouter un job CI qui échoue si le rendu et le fichier committé divergent.

Fichiers : `deploy/k8shark.yaml`, `deploy/README.md`, `helm/k8shark/templates/worker.yaml`, `helm/k8shark/templates/hub.yaml`, `Makefile`, `.github/workflows/ci.yml`

> **Note de vérification :** Dérive largement vérifiée : deploy/k8shark.yaml:156-203 a toujours hostPID, les 7 caps, debugfs/cgroup en écriture, bpffs Bidirectional (sans même --enable-tls dans les args lignes 167-172) ; RBAC lignes 62-70 (watch + endpoints/namespaces/nodes/endpointslices/deployments/daemonsets/statefulsets) vs chart hub.yaml:18-24 (pods/services/replicasets, get/list) et internal/hub/k8s.go ne fait que listPods/listReplicaSetOwners/listServices. Correction du constat : le chart ne gate PAS privileged sur tls.enabled (worker.yaml:83, values.yaml:91 privileged: true par défaut) ; seuls hostPID, les 5 caps eBPF et les monts sont gatés, et le chart ne monte jamais cgroup.

#### OPS-7 - Chart : points d'extension standards absents et noms cluster-scoped fixes qui interdisent deux installs

*impact moyen | effort M (1-3 j) | ops | confirmé*

Le chart n'expose ni imagePullSecrets (le README deploy/ documente une procédure manuelle, mais rien côté Helm), ni nodeSelector/affinity/tolerations (les tolerations du worker sont figées à operator: Exists : impossible d'exclure des noeuds GPU/spot), ni podAnnotations/extraLabels, ni digest d'image. Surtout, ClusterRole et ClusterRoleBinding s'appellent k8shark-hub-enrich en dur ({{ .Release.Name }} absent) : une deuxième release dans un autre namespace échoue sur un conflit de propriété Helm. Suffixer les ressources cluster-scoped avec le nom de release, et ajouter les blocs de values classiques repris dans les trois templates.

Fichiers : `helm/k8shark/values.yaml`, `helm/k8shark/templates/hub.yaml`, `helm/k8shark/templates/worker.yaml`, `helm/k8shark/templates/front.yaml`, `helm/k8shark/templates/_helpers.tpl`

#### OPS-8 - Défaut tag latest + pullPolicy IfNotPresent : upgrades silencieusement no-op

*impact moyen | effort S (<1 j) | ops | confirmé*

values.yaml documente lui-même le piège (lignes 10-14 : un noeud qui a déjà latest en cache ne re-pull jamais, donc helm upgrade ne déploie rien) mais livre exactement cette combinaison par défaut. Corriger dans _helpers.tpl : un helper qui rend imagePullPolicy Always quand .Values.image.tag == "latest" (ou vide) et IfNotPresent sinon, en laissant image.pullPolicy comme override explicite. À terme, faire pointer image.tag par défaut sur l'appVersion du chart (tag immuable) une fois la release automatisée en place. Impact exploitant : élimine la classe de bugs "j'ai upgradé mais rien n'a changé" sur le DaemonSet.

Fichiers : `helm/k8shark/values.yaml`, `helm/k8shark/templates/_helpers.tpl`

#### OPS-9 - /metrics Prometheus inexploitable : ni annotations de scrape ni ServiceMonitor

*impact moyen | effort S (<1 j) | ops | confirmé*

Le hub expose /metrics en texte Prometheus (internal/hub/server.go, handleMetrics) et l'endpoint reste volontairement hors auth, mais rien dans le chart ne permet de le scraper : pas d'annotations prometheus.io/scrape|port|path sur le pod hub, pas de template ServiceMonitor/PodMonitor. Ajouter les annotations par défaut sur le template du hub et un ServiceMonitor optionnel (metrics.serviceMonitor.enabled, gardé par un check .Capabilities.APIVersions sur monitoring.coreos.com/v1 pour ne pas casser helm install sans prometheus-operator). Impact exploitant : supervision du hub (drops, clients WS, taille du ring buffer) branchable sans patch manuel.

Fichiers : `helm/k8shark/templates/hub.yaml`, `helm/k8shark/values.yaml`, `internal/hub/server.go`

#### OPS-10 - CI sans test e2e du chemin de déploiement ni durcissement supply-chain

*impact moyen | effort M (1-3 j) | tests | confirmé*

La CI valide build/vet/test/helm-lint mais jamais qu'un install fonctionne : ajouter un job kind (helm/kind-action) qui charge les images buildées, fait `helm install --set worker.demo=true --wait`, puis vérifie /healthz et qu'au moins une entry arrive via /api (smoke test bout en bout du chart, des probes et du wiring worker vers hub). Côté supply-chain : golangci-lint absent (seulement gofmt+vet), pas de scan d'images (trivy-action), et docker/build-push-action est appelé sans provenance/sbom. Ces ajouts attrapent les régressions de manifests (probes, RBAC, args) que les tests Go ne voient pas.

Fichiers : `.github/workflows/ci.yml`, `helm/k8shark/`, `build/k8shark.Dockerfile`

### Sécurité

#### SEC-1 - Génération automatique d'un token API par « k8shark tap » (auth désactivée par défaut)

*impact fort | effort S (<1 j) | securite | confirmé*

Par défaut hub.apiToken est vide : tout pod du cluster peut lire l'intégralité du trafic capturé via GET /api/entries (y compris credentials présents dans les bodies), mettre la capture en pause cluster-wide via POST /api/workers/capture, ou injecter de fausses entrées via /ws/worker. « k8shark tap » n'active jamais l'auth (tap.go ne passe aucun token). Faire générer par tap un token aléatoire (crypto/rand) passé en --set hub.apiToken=... à l'install, afficher/transmettre le token à l'utilisateur, et documenter l'opt-out explicite. Le chart propage déjà le token aux workers et au front via le Secret, donc le changement est concentré dans tap.go.

Fichiers : `internal/cli/tap.go`, `helm/k8shark/values.yaml`, `internal/hub/server.go`

#### SEC-2 - Redaction des secrets Redis AUTH, params Bind Postgres et query params HTTP sensibles

*impact fort | effort M (1-3 j) | securite | confirmé*

La redaction actuelle ne couvre que les en-têtes HTTP (sensitiveHeaders dans pipeline.go). Or renderRedisCommand/redisArgs capturent en clair les arguments de AUTH, HELLO ... AUTH user pass et CONFIG SET requirepass (dans Command, Summary et RedisDetail.Args) ; pgParseBind capture les valeurs des paramètres Bind (souvent PII ou mots de passe) ; parseQuery et Path capturent les query params du type ?api_key=&access_token=. Étendre le mécanisme : masquer les arguments des commandes RESP d'authentification quand la redaction est active, ajouter une option --redact-pg-params pour remplacer PGDetail.Params par [REDACTED], et scrubber une liste de noms de query params sensibles dans parseQuery et dans Path/Summary. Ajouter les tests correspondants dans redact_test.go.

Fichiers : `internal/worker/dissect_redis.go`, `internal/worker/dissect_postgres.go`, `internal/worker/pipeline.go`, `internal/cli/worker.go`, `helm/k8shark/values.yaml`

#### SEC-3 - Worker : privileged: false par défaut, le chart liste déjà les capabilities fines

*impact fort | effort S (<1 j) | securite | confirmé*

values.yaml met worker.privileged: true par défaut alors que le template ajoute déjà NET_RAW/NET_ADMIN (+ BPF/PERFMON/etc. quand tls.enabled) : privileged rend la liste de caps inutile et donne l'accès complet aux devices de chaque nœud. AF_PACKET fonctionne avec NET_RAW en root ; passer le défaut à privileged: false, ajouter drop: [ALL] avant la liste add, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true et seccompProfile RuntimeDefault (à assouplir uniquement quand tls.enabled à cause des syscalls bpf/perf_event_open). Garder privileged en échappatoire documentée pour les CNI exotiques.

Fichiers : `helm/k8shark/values.yaml`, `helm/k8shark/templates/worker.yaml`

#### SEC-4 - Ajouter des NetworkPolicy au chart (hub joignable par n'importe quel pod)

*impact fort | effort S (<1 j) | securite | confirmé*

Le chart ne contient aucune NetworkPolicy : le Service k8shark-hub:8898 est joignable depuis tout pod du cluster, et le front (qui injecte lui-même le Bearer token dans ses requêtes proxifiées) l'est aussi, ce qui contourne l'auth même quand apiToken est défini. Ajouter un template networkpolicy.yaml (activable via values) qui restreint l'ingress du hub aux pods k8shark-worker et k8shark-front (+ un selector configurable pour le scraper Prometheus sur /metrics), et l'ingress du front à une liste d'origines configurée par l'opérateur.

Fichiers : `helm/k8shark/templates/`, `helm/k8shark/values.yaml`

#### SEC-5 - Séparer les rôles du token : lecture, contrôle et canal worker partagent le même secret

*impact moyen | effort M (1-3 j) | securite | confirmé*

Un unique apiToken donne à la fois la lecture du dashboard, le contrôle (POST /api/workers/capture, qui aveugle la capture cluster-wide) et l'accès à /ws/worker (injection d'entrées forgées, empoisonnement des données d'observabilité). Pire, le front nginx injecte ce token : quiconque atteint le front a donc aussi le contrôle. Introduire un workerToken distinct exigé sur /ws/worker, et un niveau admin pour les endpoints de contrôle (mutation) que le proxy front n'injecte pas automatiquement ; côté hub, withAuth routerait par préfixe (mutations vs lectures vs canal worker).

Fichiers : `internal/hub/server.go`, `helm/k8shark/templates/hub.yaml`, `helm/k8shark/templates/worker.yaml`, `helm/k8shark/templates/front.yaml`

#### SEC-6 - Vérification d'Origin sur les WebSockets et CORS restreint (exfiltration cross-site en port-forward)

*impact moyen | effort S (<1 j) | securite | confirmé*

CheckOrigin retourne toujours true et withCORS répond Access-Control-Allow-Origin: * ; en dev local ou pendant un port-forward sans token (le défaut), n'importe quelle page web ouverte dans le navigateur de l'opérateur peut fetch http://localhost:8898/api/entries ou ouvrir le WS et exfiltrer tout le trafic capturé du cluster. Valider par défaut l'Origin contre le Host de la requête (même origine, plus une liste --allow-origin configurable), et n'émettre les en-têtes CORS que pour ces origines au lieu du wildcard.

Fichiers : `internal/hub/server.go`, `internal/cli/hub.go`

#### SEC-7 - TLS optionnel sur le hub : token et trafic capturé transitent en clair

*impact moyen | effort M (1-3 j) | securite | confirmé*

Le hub n'écoute qu'en HTTP/ws:// : les entrées (y compris le trafic TLS déchiffré par eBPF et les bodies capturés) et le Bearer token traversent le réseau du cluster en clair entre worker, hub et front, lisibles par tout attaquant en position réseau (nœud compromis, CNI non chiffré). Ajouter --tls-cert/--tls-key au hub (ListenAndServeTLS), le support wss:// côté sink worker (websocket.Dialer avec RootCAs), un Secret TLS dans le chart (compatible cert-manager) et proxy_pass https dans le nginx du front. À défaut, documenter l'exigence d'un service mesh mTLS.

Fichiers : `internal/hub/server.go`, `internal/cli/hub.go`, `internal/worker/sink.go`, `helm/k8shark/templates/hub.yaml`, `helm/k8shark/values.yaml`

#### SEC-8 - Réduire les bornes d'allocation des dissecteurs (64 MiB par frame vs limite mémoire 512Mi)

*impact moyen | effort S (<1 j) | securite | confirmé*

maxRESPBulk et pgMaxPayload valent 64 MiB, amqpMaxFrame 16 MiB, alors que le worker a une limite mémoire de 512Mi : readPGMessage alloue le payload entier même pour des messages seulement comptés (DataRow), et un bulk RESP est lu intégralement en mémoire alors que l'affichage est tronqué à 256 octets. Un tenant qui génère des flux pod-à-pod forgés (ou un simple COPY volumineux) peut déclencher plusieurs allocations concurrentes de 64 MiB et faire OOMKill le worker, donc aveugler la capture du nœud. Remplacer l'allocation par io.CopyN(io.Discard) au-delà du nécessaire pour les types de messages non exploités, et abaisser les caps à 1-4 MiB.

Fichiers : `internal/worker/dissect_postgres.go`, `internal/worker/dissect_redis.go`, `internal/worker/dissect_amqp.go`

#### SEC-9 - Éviter le token dans l'URL (?token=) pour les WebSockets navigateur

*impact faible | effort S (<1 j) | securite | ajusté*

withAuth accepte ?token= parce qu'un navigateur ne peut pas poser d'en-tête sur un WebSocket, mais un token en query string fuit dans les logs d'accès, l'historique navigateur et les en-têtes Referer. Passer le token via le sous-protocole WebSocket (Sec-WebSocket-Protocol: bearer.<token>, que gorilla/websocket expose côté serveur) ou via un cookie de session court émis par un POST /api/session authentifié par header ; côté UI, adapter useHub.ts en conséquence et retirer le support ?token= après une période de dépréciation.

Fichiers : `internal/hub/server.go`, `ui/src/useHub.ts`

> **Note de vérification :** Le serveur accepte bien ?token= (server.go:935 et commentaire lignes 921-923) et le risque de fuite (logs de proxys, historique, Referer) est réel. Mais l'UI livrée n'envoie jamais ?token= : useHub.ts:26 construit l'URL WS avec seulement ?filter=, et grep "token" sur ui/src ne trouve rien ; en cluster c'est nginx qui pose l'en-tête Authorization sur l'upgrade WS (nginx.conf.template:22). Le chemin ?token= ne sert qu'aux clients directs hors front (port-forward manuel, cf. values.yaml:33-35). Correction : la partie « adapter useHub.ts » est sans objet aujourd'hui ; le remplacement (sous-protocole Sec-WebSocket-Protocol ou cookie de session) est purement côté hub + documentation, et il faudrait alors faire pointer les clients directs (curl/websocat, futur accès navigateur au hub nu) vers ce mécanisme avant de retirer ?token=.

### Tests et qualité

#### TST-1 - Exécuter les tests vitest du front dans la CI

*impact fort | effort S (<1 j) | tests | confirmé*

Le repo contient 8 fichiers de tests UI (~950 lignes : FilterBar, TrafficTable, EntryDetail, ServiceMap, useHub, filterParse, export, pcap) avec vitest + testing-library déjà configurés (script npm "test": "vitest run", environnement jsdom dans vite.config.ts). Mais le job "UI build" de .github/workflows/ci.yml ne lance que npm ci + npm run build : ces tests ne protègent donc rien, une régression front passe la CI. Ajouter une étape "npm test" (working-directory: ui) au job UI et une cible Makefile test-ui pour l'exécution locale. C'est le meilleur ratio valeur/effort du repo : quelques lignes de YAML activent ~950 lignes de tests existants.

Fichiers : `.github/workflows/ci.yml`, `Makefile`, `ui/package.json`

#### TST-2 - Test d'intégration bout-en-bout worker → hub → REST/WS sur de vrais WebSockets

*impact fort | effort M (1-3 j) | tests | confirmé*

server_test.go teste chaque handler isolément via httptest.NewRecorder, mais aucun test n'exerce le chemin central du produit : un worker qui se connecte en WS (/ws/worker, MsgHello), pousse des entries, et un client front (/ws) qui les reçoit filtrées via MsgFilter live, avec le round-trip de commande hub→worker (pause capture). Même le fan-out broadcast() et le drop des clients lents (broadcastDropped) ne sont testés qu'indirectement. Écrire un test Go dans internal/hub (ou un package internal/e2e) : httptest.NewServer sur les routes du hub, gorilla/websocket.Dial pour un faux worker et un faux front, injection d'entries type demo, assertions sur /api/entries, /api/summary et les frames WS reçues. Cela verrouille le contrat pkg/api (Envelope) entre les trois composants avant tout refactor, notamment les travaux pause/reprise en cours.

Fichiers : `internal/hub/server.go`, `internal/hub/server_test.go`, `internal/worker/sink.go`, `pkg/api/types.go`

#### TST-3 - Fuzzing natif Go des dissecteurs L7 et du parseur IFL

*impact fort | effort M (1-3 j) | robustesse | confirmé*

Aucun func Fuzz dans le repo alors que deux surfaces parsent des entrées hostiles : les dissecteurs worker consomment des octets réseau arbitraires (RESP récursif dans dissect_redis.go, framing AMQP, messages Postgres, sniff HTTP/TLS dans tls_pipeline.go) et une panique y tue la capture du nœud ; le parseur IFL (CompileFilter/lex dans filter.go, borné en profondeur et longueur mais jamais fuzzé) reçoit des filtres utilisateur via l'API. Ajouter FuzzCompileFilter (compile + évalue sur une entrée fixe, ne doit jamais paniquer) et FuzzConsumeStream par protocole (bytes → consumeRedisID/consumePostgresID/consumeAMQPID avec un sink de test), en seedant les corpus avec les vrais octets déjà présents dans dissect_test.go. Ajouter un job CI court (go test -fuzz sur chaque cible avec -fuzztime=30s, ou a minima exécution des corpus seed à chaque PR).

Fichiers : `internal/hub/filter.go`, `internal/worker/dissect_redis.go`, `internal/worker/dissect_postgres.go`, `internal/worker/dissect_amqp.go`, `internal/worker/tls_pipeline.go`, `internal/worker/dissect_test.go`

#### TST-4 - Activer le détecteur de course (-race) dans la CI

*impact moyen | effort S (<1 j) | tests | confirmé*

La CI lance go test ./... sans -race alors que le hub est fortement concurrent : fan-out broadcast vers N clients WS, registre workerConns partagé, ring buffer, compteurs atomiques (broadcastDropped), et le pipeline worker a son gc() concurrent. Vérifié : go test -race passe aujourd'hui en local sur hub/worker/api, donc l'activation est indolore. Remplacer l'étape par go test -race ./... dans le job Go (Linux, aucun souci de linkmode contrairement à macOS). Combiné avec le test d'intégration WS proposé, cela détectera les courses réelles du chemin broadcast/registre que les tests actuels, purement séquentiels, ne peuvent pas voir.

Fichiers : `.github/workflows/ci.yml`, `Makefile`

#### TST-5 - Ajouter golangci-lint et eslint (le lint se limite à gofmt + vet)

*impact moyen | effort M (1-3 j) | dx | confirmé*

Côté Go il n'y a ni .golangci.yml ni étape lint au-delà de gofmt/go vet : staticcheck, errcheck, ineffassign, etc. ne tournent jamais, alors que le code manipule beaucoup d'erreurs ignorées volontairement (WS writes) qu'il vaut mieux expliciter. Côté UI il n'y a aucune config eslint du tout (seul tsc -b via npm run build fait office de garde-fou) : les règles react-hooks/exhaustive-deps sont précieuses sur useHub.ts/useWorkers.ts qui gèrent reconnexion WS et effets. Ajouter golangci-lint (config minimale : govet, staticcheck, errcheck, ineffassign, misspell) via golangci-lint-action dans le job Go, une flat config eslint + typescript-eslint + eslint-plugin-react-hooks avec un script npm lint câblé dans le job UI, et une cible make lint regroupant les deux.

Fichiers : `.github/workflows/ci.yml`, `Makefile`, `ui/package.json`

#### TST-6 - Tester le serveur MCP (661 lignes, zéro test)

*impact moyen | effort M (1-3 j) | tests | confirmé*

internal/mcp/server.go implémente à la main le framing JSON-RPC-sur-stdio (handleLine, dispatch, callTool, coercitions argString/argInt) et 10+ outils qui relaient vers le hub, sans aucun test. C'est le genre de code où une régression est silencieuse : l'agent IA reçoit juste une erreur opaque. Tester avec un hub factice httptest.Server : handshake initialize + tools/list, tools/call de chaque outil avec réponses hub simulées, erreurs protocolaires (JSON malformé → -32700, méthode inconnue, outil inconnu, notification sans id), coercition d'arguments (limit numérique vs string), propagation du token hub, et comportement quand le hub est injoignable. Vérifier aussi par test que rien n'écrit sur stdout hors réponses JSON-RPC (contrainte documentée dans CLAUDE.md).

Fichiers : `internal/mcp/server.go`

#### TST-7 - Benchmarks Go des chemins chauds (fan-out, dissection, compilation IFL)

*impact moyen | effort S (<1 j) | perf | confirmé*

Aucun func Benchmark dans le repo : impossible d'objectiver une régression de perf sur les chemins qui encaissent le débit cluster. Cibles concrètes : BenchmarkBroadcast (store.add + fan-out vers N clients avec filtres compilés, le point de contention du hub), BenchmarkConsumeHTTP/Redis/Postgres (bytes réels de dissect_test.go rejoués dans le pipeline), BenchmarkCompileFilter et BenchmarkPredicate (évaluation IFL par entrée, exécutée sur chaque entry pour chaque client), avec b.ReportAllocs pour traquer les allocations par entrée. Exposer via une cible make bench ; en CI, une exécution informative (benchstat en comparaison avec main) suffit, sans seuil bloquant au début.

Fichiers : `internal/hub/server.go`, `internal/hub/filter.go`, `internal/worker/pipeline.go`

#### TST-8 - Job e2e nightly sur kind : chart Helm + capture réelle validés en vraies conditions

*impact moyen | effort L (>3 j) | ops | confirmé*

Aujourd'hui seul helm lint valide le chart, et la capture AF_PACKET/cgo n'est jamais exécutée en CI (les tests Go tournent sans privilèges ni trafic). Un workflow séparé (nightly + déclenchement manuel, pas bloquant pour les PR) créerait un cluster kind, chargerait les images buildées, installerait helm/k8shark, déploierait un pod générateur de trafic (curl en boucle vers un nginx), puis vérifierait via kubectl port-forward que /healthz répond, que /api/workers montre le worker connecté et que /api/entries contient des entries HTTP avec l'enrichissement k8s (src.name/dst.namespace remplis). C'est le seul moyen de tester RBAC, hostNetwork, capabilities du DaemonSet et le chemin AF_PACKET réel avant qu'un utilisateur ne fasse k8shark tap.

Fichiers : `.github/workflows/ci.yml`, `helm/k8shark/values.yaml`, `internal/worker/capture/afpacket_linux.go`, `internal/hub/k8s.go`

### Angles morts (critique de complétude)

#### EXT-1 - Ciblage de la capture par namespace/pod (tap targeting), la fonctionnalité phare de Kubeshark absente

*impact fort | effort L (>3 j) | feature*

Aujourd'hui chaque worker capture tout le trafic du noeud : impossible de dire « tape uniquement le namespace payments » comme le permettent Kubeshark (regex de pods, -n/-A) ou Hubble (flow filters). C'est à la fois un problème de volume (bruit, CPU, buffer hub saturé par du trafic hors sujet) et de conformité (on capture des payloads de workloads qu'on ne devrait pas voir). Approche : le hub connaît déjà les IP de pods via internal/hub/k8s.go ; il suffit de pousser des ensembles d'IP autorisées aux workers via le canal de commande hub vers worker WebSocket, et de filtrer dans route() (worker.go) ou en régénérant le filtre BPF. Exposer k8shark tap --namespaces/--pod-regex et worker.target dans les values Helm.

Fichiers : `internal/worker/worker.go`, `internal/hub/k8s.go`, `internal/cli/tap.go`, `helm/k8shark/values.yaml`, `pkg/api/types.go`

#### EXT-2 - Ingestion de fichiers PCAP hors-ligne (analyse post-mortem et dev sans Linux)

*impact moyen | effort M (1-3 j) | feature*

Le pipeline ne sait consommer que du live AF_PACKET (Linux/cgo) ; on ne peut pas rejouer une capture tcpdump/ksniff existante dans les dissecteurs. Ajouter k8shark worker --pcap-file (ou une sous-commande ingest) qui lit le fichier avec pcapgo (pur Go, donc fonctionne aussi sur macOS sans cgo) et injecte les paquets dans route() vers le hub. Valeur : post-mortem d'un incident à partir d'un pcap fourni par un client, debug des dissecteurs sur du trafic réel, et boucle de dev locale sans mode demo. Kubeshark et Wireshark couvrent ce cas ; c'est aussi la base de fixtures de tests réalistes.

Fichiers : `internal/worker/worker.go`, `internal/worker/capture/source.go`, `internal/cli/worker.go`

#### EXT-3 - Corrélation de bout en bout par trace/request ID (traceparent, x-request-id)

*impact moyen | effort M (1-3 j) | feature*

Les en-têtes HTTP sont déjà capturés (Payload.Headers) mais aucun identifiant de corrélation n'est extrait en champ de premier rang : impossible de suivre une requête à travers la chaîne front -> api -> db comme le fait Pixie. Extraire traceparent (W3C) et x-request-id dans pipeline.go vers un champ Entry.TraceID additif dans pkg/api, l'exposer en champ IFL (trace.id) et ajouter dans EntryDetail un bouton « voir toute la trace » qui applique le filtre. Va au-delà du simple filtre sur en-têtes déjà planifié : c'est le pont naturel vers les outils APM existants des utilisateurs et un différenciateur produit fort.

Fichiers : `internal/worker/pipeline.go`, `pkg/api/types.go`, `internal/hub/filter.go`, `ui/src/components/EntryDetail.tsx`

#### EXT-4 - Export continu des entries vers un système externe (JSONL, webhook, OTLP)

*impact moyen | effort M (1-3 j) | ops*

Le ring buffer du hub est un cul-de-sac : à part le WebSocket du front, rien ne permet d'envoyer le flux vers un SIEM, un data lake ou une stack logs (Hubble exporte ses flows exactement pour ça). Distinct de la persistance locale déjà planifiée : ici il s'agit d'intégration. Ajouter au hub un sink optionnel branché sur le fan-out existant (server.go) : fichier JSONL avec rotation, POST webhook par lots, voire OTLP logs. Configurable par flags (--export-file, --export-webhook) et values Helm. Ouvre les cas d'usage audit, alerting externe et rétention longue sans complexifier le hub.

Fichiers : `internal/hub/server.go`, `internal/cli/hub.go`, `helm/k8shark/values.yaml`

#### EXT-5 - Vitrine du projet : screenshots/GIF du dashboard, référence IFL complète, templates GitHub

*impact moyen | effort S (<1 j) | adoption*

Pour un produit dont l'argument principal est un dashboard temps réel, le README ne contient aucune capture d'écran ni GIF, la doc IFL se limite à cinq exemples alors que le langage a une grammaire riche (opérateurs, champs par protocole), et .github/ ne contient que ci.yml (pas d'issue templates, pas de CONTRIBUTING.md, pas de dependabot). Actions : GIF asciinema/screencast de « make dev » et du filtrage live en tête de README, page docs/ifl.md générée ou synchronisée avec le catalogue de champs de facets.go (lien depuis l'autocomplete du front), templates bug/feature et dependabot pour go.mod et ui/package.json. Coût faible, effet direct sur l'adoption et les premières contributions.

Fichiers : `README.md`, `.github/`, `internal/hub/facets.go`, `ui/src/components/FilterSuggest.tsx`

#### EXT-6 - Distribution en plugin kubectl via krew (et Homebrew) : kubectl shark tap

*impact moyen | effort M (1-3 j) | adoption*

Le canal d'installation naturel des outils de ce type dans l'écosystème k8s est krew (ksniff, ktop...) : un plugin kubectl-shark rendrait « kubectl shark tap » disponible en une commande, sans télécharger un binaire à la main. Le binaire s'y prête déjà (CLI autonome, chart Helm embarqué). Concrètement : manifest .krew.yaml, renommage/symlink kubectl-shark dans les artefacts de release, formule Homebrew pour macOS, et soumission à l'index krew. Dépend de l'automatisation des releases déjà planifiée mais constitue un chantier distinct, orienté acquisition d'utilisateurs plutôt que CI.

Fichiers : `Makefile`, `.github/workflows/ci.yml`, `cmd/k8shark/main.go`

