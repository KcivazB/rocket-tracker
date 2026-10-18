# L5–L7 — Cas d’usage, composition et livraison durable

État : implémenté dans la proposition ; intégration et qualification Windows
natives à vérifier dans la PR. E3/E4, US-02/08/09 et US-11. Les besoins/personas
restent des hypothèses du plan, sans validation utilisateur inventée.

## Contrat avant code

- Le tracker consomme des observations domaine ; les DTO restent dans Stats API.
- Record local et Ingest serveur sont deux parcours distincts, scoped explicitement.
- Un enregistrement avec destination configurée et son intention sont atomiques.
- Destination : URL, installation serveur, compte authentifié. Secret ≠ identité.
- Une absence d’identité retient l’intention. Pas de réattribution implicite.
- L’acquittement porte sur la révision lue ; rejeu idempotent après panne réseau.
- Arrêter et joindre la capture, flush tracker, tentative d’envoi bornée, joindre
  l’agent, fermer SQLite, libérer le verrou puis redémarrer après update.

## Réalisation et documentation durable

`internal/application` définit les ports étroits et les quatre cas d’usage.
SQLite implémente les écritures atomiques et le journal v8 ; v9 conserve
les origines après acquittement et sépare destinations/profils lors des fusions. Le worker vérifie
l’identité avant chaque lot de 100. Le timer local utilise RecordTraining.
L’entrée Windows délègue à `internal/desktopapp`. Les fichiers Windows et tests
sont exclus du build portable par leur suffixe ; CI Windows teste `./...`.

[Architecture](../../ARCHITECTURE.md), [ADR-0001](../../decisions/0001-architecture-hexagonale.md),
[ADR-0002](../../decisions/0002-livraison-durable.md), [opérations](../../OPERATIONS.md)
portent les contrats et la récupération. Les sources anciennes restent intactes.
Une rétrogradation exige le backup pré-migration ; un ancien binaire ignore v8.

## Scénarios et preuves reproductibles

- `go test ./internal/application ./internal/store ./internal/agent ./internal/server` :
  port échoué sans notification, validation avant persistance, rollback sur panne
  du journal, rejeu, ancienne révision, autre destination, états held/rejected,
  migration v7, copies legacy rejouables, panne serveur, rotation même compte,
  autre compte à même URL. Les tests utilisent des bases et serveurs synthétiques.
- Compilation Windows : `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build .`.
- Tests bureau : `go test ./internal/desktopapp` sur Windows ; ailleurs,
  `GOOS=windows GOARCH=amd64 go test -c ./internal/desktopapp` compile seulement.
- Restauration cohérente : test isolé et runbook L10 ; pas de RPO/RTO inventé.

Une compilation croisée n’exécute pas tray/autostart/élévation/restart. Une
qualification interactive Windows avec jeu réel et fournisseur OIDC de
production reste une preuve externe séparée ; elle ne doit pas être cochée sur
la seule base des fixtures synthétiques. Aucun merge/release/deploy autorisé.

## Revue et retrait

Principal : frontières vérifiées par imports et tests de ports/transactions.
Produit : historique local préservé, compteur de livraison et récupération
explicite ; les changements de compte ne déplacent pas les données.
SRE : rollback, rejeu, migration conservatrice et backup arrêté ; le matériel,
l’IdP et les surfaces interactives demandent leur qualification propre.

Avant retrait de cette fiche temporaire, reporter les preuves finales et
limites dans la PR/issue ; conserver les contrats dans les guides et ADR.

## Correctif configuration — handoff local du 2026-10-18

Périmètre exclusif : `internal/agent/*.go`, raccord dans
`internal/desktopapp/wizard_windows.go`, et cet ajout. Base déclarée par le
coordinateur : `33674b3` ; aucune commande Git ni publication dans ce lot.
Le store, l’application, le web et les guides partagés n’ont pas été modifiés.

### Contrat effectif

- `SaveConfig` renouvelle une génération aléatoire même si serveur/token restent
  identiques ; sur succès, le pointeur fourni par son propriétaire reçoit la
  génération persistée. `Agent.New` possède une copie profonde de sa configuration.
- `LoadConfig`, `SaveConfig`, `RemoveConfig` et le CAS de réponse heartbeat
  partagent un verrou OS sur `agent.json.lock`, conservé sur disque. Unix utilise
  `flock`, Windows `LockFileEx`. Ne pas supprimer le fichier de verrou pendant
  l’utilisation : sa fermeture, y compris à la mort du processus, libère le verrou.
  Les autres OS échouent explicitement plutôt que d’écrire sans verrou.
- Le CAS compare génération **et** serveur/token, puis applique uniquement les
  settings et l’identité serveur/compte vérifiée au fichier courant. Le verrou
  reste acquis jusqu’au renommage du nouveau fichier 0600. Il n’est jamais gardé
  pendant le réseau. Une réponse périmée ne change ni disque ni mémoire et ne lit
  aucun payload du journal. Heartbeat périodique et heartbeat du worker utilisent
  ce même parcours ; les anciens writers complets ont été supprimés.
- Les réponses de fond conservent la génération du setup. Une réponse sans
  changement ne réécrit pas le fichier. Les configurations anciennes sans
  génération restent lisibles ; le prochain `SaveConfig` en crée une.
- Le reload adopte ensemble cible, settings et état d’auto-identification, même
  à serveur/token ou mtime identiques. L’auto-identification provisoire est
  séparée des settings persistés. Les snapshots d’identité copient leurs slices.
  `uplink.Server` appelle le getter synchronisé de l’agent ; il ne partage plus
  son pointeur de configuration mutable. La déconnexion utilise `RemoveConfig`.
- La sérialisation interprocessus porte sur les writers participant à ce protocole.
  Un ancien binaire ou un éditeur qui ignore le verrou ne bénéficie pas de ce
  contrat. Le fallback outbox fichier reste une compatibilité de bibliothèque ;
  le bureau de production utilise le journal.

### Preuves réellement exécutées

Les régressions maintenues dans `config_regression_test.go` ont d’abord échoué
sur la source initiale : quatre cas canal A suspendu → `SaveConfig(B)` → libération
A, reload des settings, et propriété de la configuration. Elles passent après
correction, y compris nouvelle génération à cible identique, mtime inchangé,
lectures concurrentes, et absence de lecture de payload après réponse périmée.
`config_lock_test.go` ajoute un vrai second processus exclu au milieu du CAS,
la libération après terminaison du détenteur, la non-résurrection après suppression,
la vérification de cible indépendamment de génération, et les fichiers legacy.

Commandes depuis la racine, avec `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local` :

```sh
go test ./internal/agent -run '^TestConfig(Stale|Reload|Agent)' -count=1 -mod=readonly
go test ./internal/agent -run '^TestConfig' -count=1 -mod=readonly -v
go test ./internal/agent ./internal/application ./internal/store ./internal/server -count=1 -mod=readonly
go test -race ./internal/agent -count=1 -mod=readonly
go vet -mod=readonly ./internal/agent
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/rocket-review-qc_56em5/agent.test.exe ./internal/agent -mod=readonly
```

Résultats : premier run rouge avant correction, contrôles finaux verts sur macOS,
tests agent et verrou Windows compilés sans exécution Windows. La compilation
complète du bureau et `go test -c ./internal/desktopapp` ont échoué avant compilation
applicative : `fyne.io/systray v1.12.2` absent du cache, résolution réseau désactivée.
La CI des branches de fondation annoncée par le coordinateur ne prouve pas ce
nouveau correctif. Aucun téléchargement ni contournement de dépendance effectué.

Relecture indépendante en lecture seule du correctif v9 du coordinateur :
`record_origins` est conservé après acquittement, le lookup local filtre la
destination, et l’overlap sépare les profils explicites. Le repro original
`TestReviewTrainingMustNotMergeAcrossDestinations` conservé en overlay `/tmp`
est passé au vert, ainsi que `TestTrainingOriginsSurviveAcknowledgementAndAccountSwitch`,
`TestImportUnsupportedSchemaReportsPartialProgress` et les tests de restauration.
Cette preuve précède le dernier ajustement de clé de transport annoncé par le
coordinateur ; elle ne qualifie pas ce delta ultérieur.

### Revue et gates de sortie

- Principal, auto-revue : le même CAS possède les deux mises à jour de fond,
  sans fenêtre comparaison/rename non verrouillée. Revue indépendante Euler à
  effectuer après gel explicite ; aucune auto-approbation de sécurité.
- Produit, auto-revue : B reste sur disque après réponse A ; settings rechargés
  et identité provisoire ne restent pas attachés à l’ancien setup. Fixtures
  synthétiques, pas de compte ni de donnée réelle.
- SRE, auto-revue : verrou libéré après terminaison démontré sur macOS et CAS
  interprocessus démontré. Exécution native Windows/ACL et compilation complète
  du bureau restent des gates ; aucune preuve de panne électrique, RTO ou
  qualification de jeu réel n’est revendiquée.

Writer quiescent au handoff ; reprise des guides durables par le coordinateur.

## Contrelecture et validation d’intégration

Euler a rejoué indépendamment les trois reproductions configuration, vérifié
le manifeste avant/après et exécuté la suite agent sous race : objections levées.
La régression d’import à deux profils et son rejeu ont aussi été contre-vérifiés.
Le coordinateur a ensuite exécuté le build Windows complet, compilé les tests
`desktopapp` et lancé le vet Windows : PASS avec résolution normale des
dépendances. Cela lève le blocage de cache local de la lane, sans exécuter Windows.
La CI de la PR reste l’autorité pour les tests natifs.
