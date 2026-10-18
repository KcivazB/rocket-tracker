# L7 — Livraison durable

**Statut de préparation : proposé à la révision de référence.** L’avancement effectif se vérifie dans les PR et les preuves ci-dessous ; cette fiche seule ne prouve aucun changement livré. Base de l’audit : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3` ; revalider la révision avant exécution. Responsable et réviseur : à attribuer.

[Plan et contrats communs](../README.md) · [Audit source](https://github.com/KcivazB/rocket-tracker/issues/2)

## Rattachement et objectif

Epic **E4** ; stories **US-08, US-09**. Rendre récupérable l’intention d’envoi d’un résultat sauvegardé pour un serveur configuré.

Dépendances : Contrat L0 identité/destination et garantie décidée ; ne dépend pas du refactor complet.

Périmètre : Choisir transaction résultat+outbox ou réconciliation ; migration des files/rejets/mises à l’écart.

## Documentation avant, pendant et après

**Avant le code :** Décision de garantie et schéma ; destination serveur+compte, révision et migration/recovery. Destination ambiguë : conservation sans envoi automatique.

**Dans la PR d’implémentation :** Mettre à jour docs/ARCHITECTURE.md, docs/SELF_HOSTING.md et aide de reprise ; documenter nouvelles migrations et limites de downgrade. Les chemins cités sont des cibles documentaires ; cette fiche ne modifie pas leur contenu actuel. Une conception proposée ne remplace pas la documentation du fonctionnement livré.

**Après vérification :** joindre révision, scénarios, commandes/résultats exacts, limites et liens PR/artefact s’ils existent. Mettre à jour la matrice epic→US→lot→preuve et conserver les décisions/hypothèses ouvertes. Aucun résultat n’est acquis à la création de cette fiche.

## Critères de passage

Crash avant/après commit/ack, restart, migration rejouée, token, serveur/compte et ancien ack testés ; sources conservées jusqu’au transfert durable vérifié.

Revue architecture, impact joueur et exploitation ; ajouter la frontière sécurité lorsqu’elle est concernée. Vérifier les liens/commandes documentés. Une proposition, une intégration et une release restent des états distincts.

## État de la proposition

La mise en œuvre et ses limites sont détaillées dans [la fiche de livraison](L5-L7-complete.md). Les critères ci-dessus restent distincts de la qualification externe et de l’intégration.

## Action de référence (préparation initiale)

Décider la garantie et le traitement des anciennes entrées dont le compte historique est inconnu.


## Retrait de cette fiche temporaire

Avant retrait, conserver les décisions et leurs compromis dans les [ADR](../../decisions/0002-livraison-durable.md), les contrats livrés dans le [guide concerné](../../SELF_HOSTING.md), et les scénarios, commandes, résultats et limites dans la PR ou l’issue de suivi. Rattacher les besoins/personas validés à la documentation produit. Vérifier les liens entrants et reporter toute tâche ou décision ouverte ; cette fiche ne se supprime pas tant qu’elle porte une information nécessaire non transférée.
