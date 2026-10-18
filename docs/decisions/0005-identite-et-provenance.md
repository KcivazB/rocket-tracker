# ADR-0005 — Séparer compte, profil déclaré et preuve de partie

- Statut : Proposé
- Date : 2026-10-18
- Décisionnaire : mainteneurs pour la politique produit
- Référence : [audit de provenance et attribution](https://github.com/KcivazB/rocket-tracker/issues/2)

## Contexte

Une session OIDC identifie un compte Rocket Tracker ; un token appareil autorise des écritures dans son scope. Le profil dans le payload n’est pas une liaison plateforme vérifiée, et une liaison ne prouverait pas l’authenticité de chaque partie. Plusieurs profils d’un même utilisateur et plusieurs utilisateurs doivent rester distingués.

## Décision proposée

Présenter séparément compte expéditeur, profil déclaré/détecté, liaison plateforme réellement vérifiée si un jour implémentée, et authenticité du contenu. Le propriétaire d’écriture est résolu côté serveur, jamais délégué à un `user_id` du payload. Un GUID commun et un pseudonyme identique ne suffisent pas à lier deux perspectives ou personnes.

Conserver un premier usage local/offline honnête. Décider explicitement visibilité, classements et consentement avant de développer une intégration plateforme. Aucun badge global « vérifié » ni droit juridique de partage n’est déduit de ces contrôles techniques.

## Options et conséquences

Confondre ces identités simplifie l’affichage mais trompe sur les garanties et peut mal attribuer des données. Une liaison plateforme ajoute un flux, des droits et des limites à qualifier ; elle reste optionnelle dans ce plan tant que le besoin et l’accès ne sont pas établis. Une signature locale ou un hash ne transforme pas un JSON client en preuve officielle de participation.

## Validation et mise en œuvre

Tester profils distincts, ID forgé/non lié, rejeu, GUID copié et imports tiers sans acquisition de statut vérifié. Si une liaison est développée, vérifier ses jetons/audience/temps/session et son absence d’état partiel en erreur. Si les règles de partage changent, couvrir lectures, exports et notifications. Revoir cette décision uniquement avec un contrat produit et des preuves d’identité qualifiés.

## Mise en œuvre proposée et adoption

Le code de cette proposition et les contrats actuels sont décrits dans
[ARCHITECTURE.md](../ARCHITECTURE.md), [SPEC.md](../SPEC.md),
[OPERATIONS.md](../OPERATIONS.md) et [RELEASING.md](../RELEASING.md).
Les preuves finales sont liées dans le suivi d’implémentation et la PR.
L’autorisation d’implémenter ne vaut pas acceptation des mainteneurs, intégration
ou release. Pour ADR-0005, la séparation technique est appliquée ; politique de
partage et intégration plateforme restent des décisions produit distinctes.

### Précisions issues de l’audit #9

Le profil explicite contraint aussi la branche de déduplication par clé de
transport. L'enregistrement lit la configuration persistée et commit sous le
même verrou interprocessus que le setup ; le cache de l'uploader n'autorise pas
la destination. La fenêtre de rang conserve sa cible explicitement affichée
lors de l'ouverture. La politique d'attribution du warmup personne/profil et
la réplication des suppressions restent des décisions produit ouvertes.
