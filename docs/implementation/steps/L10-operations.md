# L10 — Budgets d'import et restauration isolée

État : **handoff local stable, writer quiescent, non publié**. Référence
de départ fournie par le coordinateur : `17c9fe7`, checkout partagé
`work/rocket-tracker-complete-delivery`. Aucune commande Git ni remote utilisée
par cette lane ; cette référence n'est pas une vérification du HEAD courant.

## Contrat et ownership

Writer unique de cette lane : `internal/server/import.go`, nouveau
`internal/server/import_limits_test.go`, nouveau `docs/OPERATIONS.md` et cette
fiche. Tracker/M03 appartient à Euler ; main, store, agent, guides runtime
partagés et CI appartiennent aux autres writers. Le coordinateur reprend les
liens/contrats dans les guides partagés. Le runbook durable est
[OPERATIONS.md](../../OPERATIONS.md), sans duplication dans SELF_HOSTING.

Objectif : borner la réception HTTP par volume, quantité de fichiers/parts,
payload temporaire, concurrence et durée ; conserver le scope utilisateur, les
types SQLite/JSON mixtes, les rejets `skipped` et les imports partiellement
appliqués. Les valeurs par défaut sont des choix de protection configurables,
pas des besoins mesurés. Une enveloppe invalide ou rejetée par les budgets de
réception ne doit écrire aucun match, jour manuel ou entraînement cible.
Les erreurs de contenu après réception ne déclenchent pas de rollback global.

API L7 reçue explicitement du coordinateur : `InstallationID`,
`PreserveLegacyOutbox`, `RecordResult`, `RecordTraining`, `PendingDeliveries`,
`CompleteDelivery`, `DeliveryStatus`, `domain.Destination` et `domain.Delivery`.
L7 apporte migration v8 et SQLite synchronous FULL ; cette lane ne les modifie
pas et ne revendique pas leur implémentation. L'exercice restaure une copie
arrêtée, vérifie identité/révision/payload et destinations, et n'envoie rien.

## Phases et scénarios

1. Lecture de l'import, des callers/tests et du contrat d'ingestion : seule
   l'enveloppe 1 GiB était bornée ; chaque part était appliquée avant réception
   complète. Pas d'index GitNexus présent ; exploration sur sources courantes.
   Recherche RAG locale indisponible (`local Ollama embedding request failed`).
2. Tests de comportement ajoutés avant patch : rouge constaté sur les budgets
   fichiers/temp/body/config ignorés, épilogue non borné et import tronqué
   laissant un match appliqué. Le scénario frontière/rejeu passait déjà.
3. Réception complète dans un répertoire privé borné, admission globale sans
   attente, délai HTTP/SQL, champs d'erreur et compteurs partiels. Les valeurs
   explicites et règles métier d'ingestion restent dans leurs owners. Les deux
   chemins training (`QueueEntry` et `Database`) passent désormais par
   `application.IngestTraining(ctx, im.Scope, key, training)` à la demande du
   coordinateur ; aucun appel direct à AddTraining dans cet import.
4. Régressions : limites exactes et +1, body de longueur inconnue, champs
   multipart ignorés comptés, annulation libérant le slot, lecture HTTP lente
   interrompue, JSON volumineux toujours `skipped`, scope et replay, erreur de
   contenu après un succès avec compteurs partiels, nettoyage temporaire.
5. Restauration : store réel fermé/copie/réouverture, manifestes SHA-256 en
   mémoire, identité serveur/device et paramètres, destinations URL/installation/
   compte distinctes, payload et révision inchangés, ancien ack sans suppression,
   historiques inconnus conservés held. Un second scénario SQLite synthétique
   couvre spécifiquement une ligne présente uniquement dans le WAL arrêté.

## Preuves exécutées et limites

Hôte local : Darwin/arm64 ; `go version` : `go1.27.2 darwin/arm64`, compatible
avec le minimum `go 1.26.0` du module. Dépendances locales uniquement,
`GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`, `-mod=readonly`. Aucune donnée réelle.

| Commande | Résultat observé |
| --- | --- |
| `go test ./internal/server -run 'TestImportUpload\|TestImporterPath\|TestDatabaseImport' -count=1 -mod=readonly` (avant ajout des tests) | PASS, package server 0.663s. |
| `go test ./internal/server -run '^TestImportLimits' -count=1 -mod=readonly` (tests nouveaux, production initiale) | FAIL attendu : fichiers/temp/body/config ignorés, épilogue accepté ; multipart tronqué avait déjà écrit un match. |
| `go test ./internal/server -run 'TestImport\|TestDatabaseImport\|TestOperations' -count=1 -mod=readonly` | PASS après patch et API L7, 1.320s, avant derniers cas additionnels de configuration/JSON volumineux. |
| `go test ./internal/application ./internal/server ./internal/store ./internal/agent ./cmd/rltracker-server -count=1 -mod=readonly` | PASS après raccordement IngestTraining : application 0.214s, server 3.637s, store 1.217s, agent 1.138s ; commande serveur compilée, sans tests. |
| `go test -race ./internal/server -run 'TestImport\|TestDatabaseImport\|TestOperations' -count=1 -mod=readonly` | PASS après raccordement, 4.684s. |
| `go vet -mod=readonly ./internal/server ./cmd/rltracker-server` | PASS, aucune sortie. |
| `go test ./internal/server -run 'TestImportLimitsRejectBeforeWrites/epilogue\|TestOperations' -count=1 -mod=readonly` | PASS, 0.710s après renforcement final de l'épilogue sans Content-Length et des assertions de restauration. |
| `go test ./internal/server -coverprofile="$task_cov" -count=1 -mod=readonly` (`task_cov` créé par `mktemp /tmp/rocket-l10-cover.XXXXXX`) | PASS final, 3.068s, couverture package 76.1 %. Budgets/admission/erreurs 100 %, staging 82.8 %, handler 89.7 %, importStaged 81.2 %. Pas de baseline de couverture globale revendiquée. |
| `go build -mod=readonly -o "$task_build/rltracker-server" ./cmd/rltracker-server` (`task_build` créé par `mktemp -d /tmp/rocket-l10-build.XXXXXX`) | PASS natif ; aucun serveur lancé. |
| Vérification Python standard-library des liens relatifs et whitespace des deux guides | PASS avant clôture documentaire ; vérification finale à nouveau ci-dessous. |

Deux essais intermédiaires étaient bloqués par des écritures concurrentes
store/domaine/tracker : API de journal ou types d'observation temporairement
absents. Aucun contournement ni modification de ces owners. Une erreur de
compilation de mon test (`DeviceUser` retourne trois valeurs) a été corrigée
avant le PASS ci-dessus.

Budgets exacts : réception HTTP et payloads de staging, **pas quota global
disque/RSS ni nombre de lignes SQL**. Les migrations/réparations et
checkpoint/close de `store.Open` ne sont pas contextualisés : leur durée et
leurs fichiers de travail peuvent dépasser les limites de réception. Cette
limite doit rester dans le runbook et le handoff ; aucune promesse de deadline
murale stricte. Une interruption réseau peut empêcher le retour des compteurs.
Le CLI d'import garde son contrat sans acquérir ces budgets HTTP.

Non exécuté : production, vrai IdP/agents, rétablissement d'un hôte réel,
Windows/ACL, Docker/proxy, charge/capacité, incident matériel, RTO/RPO. Aucun
objectif chiffré de récupération ni publication revendiqué.

## Revue et handoff

Régression pertinente, race ciblé, vet, build natif et restauration synthétique
exécutés. Cible de code finale (SHA-256, sans Git) :

```text
internal/server/import.go ff72c23eec061ce4b77f81e72664f8e130cd3cba26449fbe79133cbce5c81555
internal/server/import_limits_test.go a111cdaa9c2d10730b2ee6c200321509e2e2bb0e3bc2f72540ef18e37d82b489
```

Revue indépendante par le coordinateur requise ; la messagerie disponible a
refusé l'envoi vers l'ancêtre natif, donc aucune demande n'a été délivrée. Les
lenses suivantes sont une auto-relecture, pas une approbation indépendante :

| Lens | État | Gate |
| --- | --- | --- |
| Principal Engineer | PASS sur le contrat local, auto-relecture | Scopes, types mixtes, replay et ack de révision couverts ; enveloppe bornée précède les écritures cible. Revue indépendante du patch reste ouverte. |
| Product Engineer | PASS sur le contrat local, auto-relecture | Erreurs actionnables et compteurs de succès partiel testés ; pas de promesse all-or-nothing ou capacité mesurée. Revue indépendante reste ouverte. |
| SRE | BLOCKED pour qualification globale ; preuves synthétiques PASS | Restauration/identités/WAL/journal et timeout de lecture passent. Pas de quota FS/RSS ni deadline stricte de migration/close, pas de qualification environnement réel. |

Gates nommés : **L10-G1 — borne stricte de travail SQLite** (API store
contextualisée et budget des fichiers de travail/quota host à traiter avec son
owner si exigés) ; **L10-G2 — revue indépendante du patch stable** (coordinateur) ;
**L10-G3 — qualification de restauration de l'environnement cible** (owner
exploitation ; IdP/Windows/ACL/proxy/capacité et journaux dépassant les 100 entrées
retournées par PendingDeliveries). Ces gates n'empêchent pas le handoff local,
mais empêchent une affirmation de protection ou récupération globale acquise.

Le coordinateur est owner de l'intégration et de la revue indépendante. Aucun
Git, commit, remote, PR, publication, release ou déploiement dans cette lane.

## Correction après revue indépendante

La revue croisée a reproduit un panic sur une base SQLite dont `user_version`
est négatif. L’ouverture refuse désormais les versions négatives et futures
avant d’indexer les migrations.
`TestImportUnsupportedSchemaReportsPartialProgress` vérifie les deux cas avec
un premier fichier valide : HTTP 400, un match déjà importé annoncé, répertoire
temporaire nettoyé. `go test ./internal/store ./internal/server` : PASS.
Les limites SQLite de durée et de travail disque du runbook restent applicables.
