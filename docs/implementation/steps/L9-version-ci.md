# L9 — Version et CI

**Statut de préparation : proposé à la révision de référence.** L’avancement effectif se vérifie dans les PR et les preuves ci-dessous ; cette fiche seule ne prouve aucun changement livré. Base de l’audit : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3` ; revalider la révision avant exécution. Responsable et réviseur : à attribuer.

[Plan et contrats communs](../README.md) · [Audit source](https://github.com/KcivazB/rocket-tracker/issues/2)

## Rattachement et objectif

Epic **E6** ; stories **Transverse US-01 à US-10**. Unifier le contrat de version et vérifier le candidat exact avant une publication autorisée.

Dépendances : Convention de version adoptée ; coordonner workflows avec L3 et entrées avec L6.

Périmètre : Une variable `buildinfo.Version` commune aux deux exécutables, scripts, Docker, CI, updater et guides concernés.

## Documentation avant, pendant et après

**Avant le code :** Décision de version, matrice des consommateurs, schéma Build/Tests/Release et identité des artefacts.

**Dans la PR d’implémentation :** Adapter docs/DEVELOPMENT.md, la règle AGENTS.md et les procédures d’update/publication dans la PR de changement de politique. Les chemins cités sont des cibles documentaires ; cette fiche ne modifie pas leur contenu actuel. Une conception proposée ne remplace pas la documentation du fonctionnement livré.

**Après vérification :** joindre révision, scénarios, commandes/résultats exacts, limites et liens PR/artefact s’ils existent. Mettre à jour la matrice epic→US→lot→preuve et conserver les décisions/hypothèses ouvertes. Aucun résultat n’est acquis à la création de cette fiche.

## Critères de passage

Build simple/injecté sur les deux exécutables, normalisation et updater vérifiés ; Node explicite ; mêmes octets testés et publiés si release autorisée.

Revue architecture, impact joueur et exploitation ; ajouter la frontière sécurité lorsqu’elle est concernée. Vérifier les liens/commandes documentés. Une proposition, une intégration et une release restent des états distincts.

## État de la proposition

La mise en œuvre et ses limites sont détaillées dans [la fiche de livraison](L9-artifacts.md). Les critères ci-dessus restent distincts de la qualification externe et de l’intégration.

## Action de référence (préparation initiale)

Appliquer [ADR-0003](../../decisions/0003-version-et-artefacts.md) : supprimer les bumps de sources et migrer les injections vers `buildinfo.Version`, avec fallback `dev` et tests des binaires.


## Retrait de cette fiche temporaire

Avant retrait, conserver les décisions et leurs compromis dans les [ADR](../../decisions/0003-version-et-artefacts.md), les contrats livrés dans le [guide concerné](../../DEVELOPMENT.md), et les scénarios, commandes, résultats et limites dans la PR ou l’issue de suivi. Rattacher les besoins/personas validés à la documentation produit. Vérifier les liens entrants et reporter toute tâche ou décision ouverte ; cette fiche ne se supprime pas tant qu’elle porte une information nécessaire non transférée.
