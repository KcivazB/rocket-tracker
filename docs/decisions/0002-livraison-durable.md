# ADR-0002 — Enregistrer résultat et intention dans une transaction SQLite

- Statut : Proposé ; option transactionnelle implémentée dans cette PR
- Date : 2026-10-18
- Référence : [audit, architecture et reprise](https://github.com/KcivazB/rocket-tracker/issues/2)

## Contexte

La sauvegarde SQLite et l’outbox fichier étaient deux écritures indépendantes. Un arrêt entre les deux pouvait laisser un résultat uniquement local. Le serveur de destination ne prouvait pas le compte historique de chaque entrée. Le token courant ne reconstitue pas cette identité.

## Décision

La migration v8 ajoute `delivery_intents` ; la v9 conserve dans `record_origins` la destination d’origine de chaque enregistrement local, même après acquittement. Une fusion locale exige la même destination ; deux profils explicitement différents ne fusionnent pas leurs entraînements. Une origine historique ambiguë reste séparée des nouvelles écritures. `Scope.RecordResult` et `RecordTraining` enregistrent le résultat normalisé et son intention d’envoi dans **la même transaction**. Un échec du journal annule la transaction. Les écritures serveur `Ingest`/`AddTraining` ne créent aucune intention de renvoi. SQLite utilise WAL et `synchronous(FULL)` ; cela ne dispense pas d’un backup cohérent ni ne garantit le matériel de stockage.

Une destination comprend l’URL normalisée, l’identifiant persistant de l’installation serveur et l’identifiant du compte renvoyés par un heartbeat authentifié. Le token reste un secret d’authentification. Le worker vérifie cette identité avant chaque lot et ne lit que les intentions de cette destination. Une rotation pour le même compte permet la reprise ; un autre compte ou une autre installation n’hérite pas de la file.

La clé de transport dérive de l’installation locale, du propriétaire, du type, de l’identifiant local et de la destination. Une modification du payload incrémente sa révision. L’acquittement ou le rejet compare destination, propriétaire et révision. Un acquittement tardif ne supprime pas une modification plus récente.

Une configuration présente mais sans identité confirmée produit une intention `held`. Une configuration illisible échoue explicitement. Sans configuration serveur, le parcours reste local. Le statut local affiche les nombres pending/held/rejected/legacy_held.

## Migration et récupération

Les documents des anciennes files normales, rejetées et mises à l’écart sont copiés de façon rejouable dans `legacy_deliveries`, sans effacer les sources ni attribuer un compte. Les chemins et contenus permettent de distinguer les sources ; les limites sont 10 000 documents et 64 MiB cumulés par répertoire, et 4 MiB par document. Une limite dépassée arrête la migration avec une erreur ; les copies déjà faites et les sources sont conservées pour reprise.

La récupération est explicite : après vérification du propriétaire et de la destination, importer les fichiers conservés ou la base locale dans le compte choisi (`agent import` ou import du dashboard). Les intentions inconnues ne sont jamais automatiquement rattachées. Le journal conserve leur état jusqu’à une intervention opérateur ; voir [le runbook](../OPERATIONS.md).

**Rétrogradation :** un ancien binaire ignore le journal v8 et ne livre pas les intentions. Arrêter l’application et conserver un backup complet avant changement de version. Pour revenir à une ancienne version, restaurer le backup pré-migration dans une instance isolée ; ne pas modifier `user_version` ni supprimer les tables sur une base utilisée.

## Options et compromis

La réconciliation de deux écritures demanderait une nouvelle source de vérité et des preuves pour chaque fenêtre de panne. La transaction exploite la base existante et supprime cette fenêtre. Le worker et une migration sont nécessaires ; l’ancienne outbox reste un format d’import et une compatibilité de bibliothèque, pas une seconde file active du bureau.

La limite de lot est 100 intentions. Le réseau est hors transaction ; un crash après réception et avant acquittement entraîne un rejeu, traité par les clés d’ingestion et l’identité métier. Les modifications du dashboard ne constituent pas une synchronisation bidirectionnelle ; elles suivent le contrat décrit dans [ARCHITECTURE.md](../ARCHITECTURE.md).

## Preuves

`internal/store/delivery_test.go` injecte un échec de journal, vérifie rollback, rejeu, révision tardive, mauvais compte/serveur, migration v7 et réouverture. `internal/agent/journal_test.go` vérifie panne, rotation et changement de compte contre un serveur HTTP synthétique. Le runbook possède une restauration synthétique séparée. Ces tests ne qualifient ni une panne électrique réelle ni un fournisseur OIDC de production.

## Mise en œuvre proposée et adoption

Le code de cette proposition et les contrats actuels sont décrits dans
[ARCHITECTURE.md](../ARCHITECTURE.md), [SPEC.md](../SPEC.md),
[OPERATIONS.md](../OPERATIONS.md) et [RELEASING.md](../RELEASING.md).
Les preuves finales sont liées dans le suivi d’implémentation et la PR.
L’autorisation d’implémenter ne vaut pas acceptation des mainteneurs, intégration
ou release. Pour ADR-0005, la séparation technique est appliquée ; politique de
partage et intégration plateforme restent des décisions produit distinctes.

### Précisions issues de l’audit #9

La proposition distingue désormais une origine locale explicite, une origine
historique inconnue et une destination configurée. Toutes les recherches de
candidats respectent cette séparation, après acquittement comme avant. La v10
ajoute les clés sources fusionnées des entraînements pour conserver le rejeu
lorsqu'une fusion dépasserait douze heures. Les snapshots avant commit restent
la propriété en mémoire du tracker jusqu'au succès ; cela ne promet pas une
persistance après arrêt forcé. Voir les contrats actuels dans ARCHITECTURE et
OPERATIONS, et les régressions de persistance/tracker.
