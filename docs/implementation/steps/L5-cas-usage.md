# L5 — Cas d’usage locaux et serveur

**Statut de préparation : proposé à la révision de référence.** L’avancement effectif se vérifie dans les PR et les preuves ci-dessous ; cette fiche seule ne prouve aucun changement livré. Base de l’audit : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3` ; revalider la révision avant exécution. Responsable et réviseur : à attribuer.

[Plan et contrats communs](../README.md) · [Audit source](https://github.com/KcivazB/rocket-tracker/issues/2)

## Rattachement et objectif

Epic **E3** ; stories **US-02, US-08, US-09**. Séparer Record local et Ingest serveur, sans changer implicitement les garanties d’écriture/reprise.

Dépendances : L4 ; sérialiser l’orchestration partagée avec L6/L7.

Périmètre : Ports étroits et adaptateurs existants ; scope autorisé explicite ; pas de boucle d’envoi serveur.

## Documentation avant, pendant et après

**Avant le code :** Séquences nominales/pannes, ports, contexte autorisé et résultat de chaque effet.

**Dans la PR d’implémentation :** Mettre à jour docs/ARCHITECTURE.md et les contrats d’ingestion, y compris les limites DB/file. Les chemins cités sont des cibles documentaires ; cette fiche ne modifie pas leur contenu actuel. Une conception proposée ne remplace pas la documentation du fonctionnement livré.

**Après vérification :** joindre révision, scénarios, commandes/résultats exacts, limites et liens PR/artefact s’ils existent. Mettre à jour la matrice epic→US→lot→preuve et conserver les décisions/hypothèses ouvertes. Aucun résultat n’est acquis à la création de cette fiche.

## Critères de passage

Doubles DB/file, agent arrêté, échecs distincts ; upload/import scoped, profils/rejeu/abandon→final et éditions préservés ; aucun cycle.

Revue architecture, impact joueur et exploitation ; ajouter la frontière sécurité lorsqu’elle est concernée. Vérifier les liens/commandes documentés. Une proposition, une intégration et une release restent des états distincts.

## État de la proposition

La mise en œuvre et ses limites sont détaillées dans [la fiche de livraison](L5-L7-complete.md). Les critères ci-dessus restent distincts de la qualification externe et de l’intégration.

## Action de référence (préparation initiale)

Définir les commandes et issues du premier parcours vertical puis ses doubles.


## Retrait de cette fiche temporaire

Avant retrait, conserver les décisions et leurs compromis dans les [ADR](../../decisions/0001-architecture-hexagonale.md), les contrats livrés dans le [guide concerné](../../ARCHITECTURE.md), et les scénarios, commandes, résultats et limites dans la PR ou l’issue de suivi. Rattacher les besoins/personas validés à la documentation produit. Vérifier les liens entrants et reporter toute tâche ou décision ouverte ; cette fiche ne se supprime pas tant qu’elle porte une information nécessaire non transférée.
