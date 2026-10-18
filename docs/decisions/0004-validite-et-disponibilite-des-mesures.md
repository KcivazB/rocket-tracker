# ADR-0004 — Distinguer quantité valide, zéro observé et absence

- Statut : Proposé
- Date : 2026-10-18
- Décisionnaire : mainteneurs
- Référence : [audit, cas S1 et M-03 à M-06](https://github.com/KcivazB/rocket-tracker/issues/2)

## Contexte

L’ingestion acceptait des quantités négatives. Des états de voiture partiels produisaient aussi des mesures artificielles par dénominateur commun. Les coordonnées et différences de buts peuvent être signées ; des anciens enregistrements n’ont pas tous les champs. Les unités d’une capture brute ne se déduisent pas de la seule amplitude.

## Décision proposée

Valider les quantités impossibles par invariant partagé entre upload et import, avant écriture du résultat. Conserver zéros explicites, positions signées et données historiques/partielles compatibles. Un refus d’enregistrement n’implique pas un rollback global de tout import.

Pour les mesures dérivées, conserver disponibilité et dénominateur propres, ou exiger un paquet minimal documenté avant de produire les métriques communes. Un booléen omis n’est pas un `false` mesuré. Le choix du paquet doit préserver les observations complètes et ne pas diluer leur résultat avec des états incomplets.

Nommer population, unité, effectif et pondération. Une moyenne de matchs et une moyenne de temps répondent à des questions différentes. Ne pas réinterpréter l’historique ou changer les conversions tant que l’unité brute reste non qualifiée ; ne pas transformer une association en causalité sportive.

## Options et conséquences

Rejeter tous les nombres négatifs casserait les positions. Remplacer toute absence par zéro fabrique une précision. Un paquet minimal est plus petit qu’une migration de tous les champs vers des valeurs optionnelles mais renonce à exploiter certaines observations partielles ; les dénominateurs par métrique coûtent davantage et conservent plus d’information. Trancher dans le périmètre de chaque métrique avec preuve.

## Validation et mise en œuvre

Fixtures absent/zéro/signé, champs partiels/complets alternés, rejouabilité/import, mauvaise population et unités qualifiées. Vérifier la restitution stockée et les libellés FR/EN. Des tests synthétiques de conversion ne prouvent pas l’unité émise par le jeu. Revoir si une source qualifiée ou un besoin d’analytique partielle impose une représentation plus précise.

## Mise en œuvre proposée et adoption

Le code de cette proposition et les contrats actuels sont décrits dans
[ARCHITECTURE.md](../ARCHITECTURE.md), [SPEC.md](../SPEC.md),
[OPERATIONS.md](../OPERATIONS.md) et [RELEASING.md](../RELEASING.md).
Les preuves finales sont liées dans le suivi d’implémentation et la PR.
L’autorisation d’implémenter ne vaut pas acceptation des mainteneurs, intégration
ou release. Pour ADR-0005, la séparation technique est appliquée ; politique de
partage et intégration plateforme restent des décisions produit distinctes.

### Précisions issues de l’audit #9

Les cohortes comparables distinguent variante, mode et duel. La disponibilité des
démolitions dépend de leur propre tableau. Toutes les inférences temporelles
historiques partagent le validateur de chronologie ; les buckets « minute » font
soixante secondes et les rythmes comparés utilisent le même temps réglementaire.
Les pauses du minuteur suivent [ADR-0006](0006-temps-actif-et-frontieres.md).
Les règles exécutables et leurs limites figurent dans SPEC et les tests web.
