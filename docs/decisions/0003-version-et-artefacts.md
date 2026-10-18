# ADR-0003 — Une version commune injectée au build

- Statut : Proposé
- Date : 2026-10-18
- Décisionnaire : mainteneurs
- Référence : [audit des versions et LDFLAGS](https://github.com/KcivazB/rocket-tracker/issues/2)

## Contexte

À la révision auditée, les deux exécutables déclarent un numéro de release distinct dans leur package main et acceptent une injection `-X main.Version`. La règle de bump commun fait modifier deux fichiers pour chaque correctif, alors que les scripts de release disposent déjà du tag. Un build simple peut ainsi annoncer le numéro d’une release sans en être l’artefact publié.

## Décision proposée

Centraliser la variable dans `internal/buildinfo` : `var Version = "dev"`. Les deux exécutables et leurs consommateurs (CLI, logs, HTTP, heartbeat et updater) lisent cette variable. Les builds injectent leur version avec `-ldflags "-X rocket-tracker/internal/buildinfo.Version=1.2.3"`. Retirer la règle de bump de sources et le suffixe de version obligatoire dans les commits.

Les releases utilisent leur tag sans `v`. `build.ps1 -Version` et `docker build --build-arg VERSION=…` permettent l’injection explicite ; sans injection, le binaire annonce `dev`. L’updater existant ignore les versions de développement non numériques : conserver et tester ce comportement. Aucun numéro de release ne vit dans les sources Go.

Mettre à jour les scripts, Docker, workflows et guides dans le même changement. **Compatibilité :** les scripts externes utilisant `-X main.Version` doivent adopter le nouveau chemin ; un ancien flag ne doit pas être présenté comme compatible. Les guides expliquent ce transfert et les builds vérifient la valeur réellement annoncée.

## Options et conséquences

Garder deux littéraux maintient une coordination évitable. Deux variables main avec un fallback constant supprimeraient les bumps mais conserveraient deux points de configuration. Un package unique est justifié par les deux exécutables et rend la cible du linker commune. Ajouter une déduction automatique par Git n’est pas nécessaire pour ce contrat : le build choisit explicitement sa version.

L’injection d’une chaîne ne prouve pas qu’un binaire correspond à une release. La traçabilité des artefacts reste un volet séparé : Build et Tests distincts des autorisations Release/Deploy, candidat identifié et publication des mêmes octets que ceux vérifiés. La correction de version seule ne démontre pas ce volet complet.

## Validation et mise en œuvre

Construire et exécuter le serveur sans injection (`dev`) et avec injection (`1.2.3`). Construire le desktop Windows avec les deux modes ; exécuter les tests Windows séparément sur une machine adaptée. Vérifier l’absence de requête updater pour `dev`, ainsi que toutes les références de linker dans les scripts et exemples. Aucun tag, release ou déploiement n’est créé pour vérifier ce contrat.

Les PR précisent les preuves exécutées et les environnements non qualifiés. Revoir si un consommateur externe exige une transition de chemin linker ou un format de version différent. Le statut proposé reste distinct d’une intégration ou release.

## Mise en œuvre proposée et adoption

Le code de cette proposition et les contrats actuels sont décrits dans
[ARCHITECTURE.md](../ARCHITECTURE.md), [SPEC.md](../SPEC.md),
[OPERATIONS.md](../OPERATIONS.md) et [RELEASING.md](../RELEASING.md).
Les preuves finales sont liées dans le suivi d’implémentation et la PR.
L’autorisation d’implémenter ne vaut pas acceptation des mainteneurs, intégration
ou release. Pour ADR-0005, la séparation technique est appliquée ; politique de
partage et intégration plateforme restent des décisions produit distinctes.
