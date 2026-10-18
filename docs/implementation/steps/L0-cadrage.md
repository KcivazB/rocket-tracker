# L0 — Cadrage et référence

**Statut de préparation : proposé à la révision de référence.** L’avancement effectif se vérifie dans les PR et les preuves ci-dessous ; cette fiche seule ne prouve aucun changement livré. Base de l’audit : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3` ; revalider la révision avant exécution. Responsable et réviseur : à attribuer.

[Plan et contrats communs](../README.md) · [Audit source](https://github.com/KcivazB/rocket-tracker/issues/2)

## Rattachement et objectif

Epic **E0** ; stories **US-01 à US-11 proposée**. Réconcilier l’audit avec la tête retenue et préparer les contrats exécutables.

Dépendances : Aucune. La requalification peut se faire par contrat sans bloquer tous les correctifs.

Périmètre : Rapport, guides et tests du checkout choisi ; personas, epics et US du plan.

## Documentation avant, pendant et après

**Avant le code :** Révision, matrice constat→story→lot→test ; architecture actuelle et cible séparées ; décisions/responsables ouverts.

**Dans la PR d’implémentation :** Corriger les guides seulement sur les faits vérifiés ; conserver les propositions dans le dossier de conception. Les chemins cités sont des cibles documentaires ; cette fiche ne modifie pas leur contenu actuel. Une conception proposée ne remplace pas la documentation du fonctionnement livré.

**Après vérification :** joindre révision, scénarios, commandes/résultats exacts, limites et liens PR/artefact s’ils existent. Mettre à jour la matrice epic→US→lot→preuve et conserver les décisions/hypothèses ouvertes. Aucun résultat n’est acquis à la création de cette fiche.

## Critères de passage

Chaque lot engagé a un scénario discriminant, un résultat attendu et un propriétaire de règle ; faits historiques requalifiés.

Revue architecture, impact joueur et exploitation ; ajouter la frontière sécurité lorsqu’elle est concernée. Vérifier les liens/commandes documentés. Une proposition, une intégration et une release restent des états distincts.

## État de la proposition

La mise en œuvre et ses limites sont détaillées dans [la fiche de livraison](../REQUALIFICATION.md). Les critères ci-dessus restent distincts de la qualification externe et de l’intégration.

## Action de référence (préparation initiale)

Relever HEAD et associer les premiers cas S1/S2 à leurs tests courants.


## Retrait de cette fiche temporaire

Avant retrait, conserver les décisions et leurs compromis dans les [ADR](../../decisions/README.md), les contrats livrés dans le [guide concerné](../../ARCHITECTURE.md), et les scénarios, commandes, résultats et limites dans la PR ou l’issue de suivi. Rattacher les besoins/personas validés à la documentation produit. Vérifier les liens entrants et reporter toute tâche ou décision ouverte ; cette fiche ne se supprime pas tant qu’elle porte une information nécessaire non transférée.
