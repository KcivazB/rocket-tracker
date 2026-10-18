# L8 — Métier et états UX

**Statut de préparation : proposé à la révision de référence.** L’avancement effectif se vérifie dans les PR et les preuves ci-dessous ; cette fiche seule ne prouve aucun changement livré. Base de l’audit : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3` ; revalider la révision avant exécution. Responsable et réviseur : à attribuer.

[Plan et contrats communs](../README.md) · [Audit source](https://github.com/KcivazB/rocket-tracker/issues/2)

## Rattachement et objectif

Epic **E5** ; stories **US-01, US-03 à US-07, US-10**. Corriger le bon profil, les populations et les messages sans promettre une causalité sportive.

Dépendances : Cas L0 encore reproduit ; décision propre à la métrique ou à la visibilité si nécessaire.

Périmètre : Une PR par invariant ; commencer absence/identité puis éligibilité/prédicats/aide ; M-04 exige unité qualifiée.

## Documentation avant, pendant et après

**Avant le code :** Story, fixture et contrat de mesure (unité/population/dénominateur/effectif) ; maquette des états utiles.

**Dans la PR d’implémentation :** Mettre à jour docs/SPEC.md, aide FR/EN et contrat d’identité/partage ; garder la justification dans une décision. Les chemins cités sont des cibles documentaires ; cette fiche ne modifie pas leur contenu actuel. Une conception proposée ne remplace pas la documentation du fonctionnement livré.

**Après vérification :** joindre révision, scénarios, commandes/résultats exacts, limites et liens PR/artefact s’ils existent. Mettre à jour la matrice epic→US→lot→preuve et conserver les décisions/hypothèses ouvertes. Aucun résultat n’est acquis à la création de cette fiche.

## Critères de passage

Fixture et voisins vérifiés ; profil correct et états local/en attente/livré explicites ; DOM/notifications/jeu annoncés selon preuve réelle.

Revue architecture, impact joueur et exploitation ; ajouter la frontière sécurité lorsqu’elle est concernée. Vérifier les liens/commandes documentés. Une proposition, une intégration et une release restent des états distincts.

## État de la proposition

La mise en œuvre et ses limites sont détaillées dans [la fiche de livraison](L8-analytics-complete.md). Les critères ci-dessus restent distincts de la qualification externe et de l’intégration.

## Action de référence (préparation initiale)

Sélectionner un cas requalifié et convenir de son oracle avant de modifier le calcul.


## Retrait de cette fiche temporaire

Avant retrait, conserver les décisions et leurs compromis dans les [ADR](../../decisions/0004-validite-et-disponibilite-des-mesures.md), les contrats livrés dans le [guide concerné](../../SPEC.md), et les scénarios, commandes, résultats et limites dans la PR ou l’issue de suivi. Rattacher les besoins/personas validés à la documentation produit. Vérifier les liens entrants et reporter toute tâche ou décision ouverte ; cette fiche ne se supprime pas tant qu’elle porte une information nécessaire non transférée.
