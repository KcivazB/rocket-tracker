# ADR-0006 — Temps actif des entraînements

- Statut : **Proposé**
- Contexte : audit de la PR #8, finding PROD-02 de l’issue #9.

## Contexte

Les timestamps d'un minuteur contiennent les pauses. Les remplacer par une durée
compressée détruirait la chronologie et ne représenterait pas les blocs repris.
Les anciennes séances n'ont pas de mesure explicite des pauses.

## Décision proposée

Conserver l'enveloppe temporelle et ajouter `active_intervals`, liste de couples
`started_at`/`ended_at`. Absence ou null conserve la convention historique de
pleine durée ; une liste vide exprime zéro activité mesurée. Valider les bornes,
borner le nombre d'intervalles et calculer la durée de leur union. Canoniser
les bornes et intervalles en secondes UTC entières avant de décider les seuils
côté navigateur, comme lors de la persistance ; ne pas promettre une durée
fractionnaire puis en enregistrer une autre. La fusion ne
crée jamais une enveloppe supérieure à douze heures ; conserver plusieurs lignes
quand cette contrainte interdit la fusion.

## Alternatives et conséquences

Une simple durée ne permet pas de calculer une union après rejeu. Raccourcir la
fin ferait croire que la séance s'est terminée plus tôt. Les intervalles ajoutent
un champ JSON facultatif, compatible avec les données historiques. Ils ne
permettent pas de reconstruire les pauses manquantes des anciennes données.

## Mise en œuvre et vérification

La proposition est implémentée dans domaine/store et le minuteur web ; voir les
tests d'intervalles du domaine, les régressions de persistance et les scripts
`web/tests/`. L'acceptation de l'ADR reste une décision des mainteneurs, distincte
des tests, de la revue et d'une mise en production.
