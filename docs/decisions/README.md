# Décisions d’architecture

Les ADR conservent les décisions durables, leurs raisons et leurs compromis. Les guides [d’architecture](../ARCHITECTURE.md), [de développement](../DEVELOPMENT.md) et [d’exploitation](../SELF_HOSTING.md) décrivent le comportement effectivement implémenté.

| ADR | Sujet | Statut |
|---|---|---|
| [0001](0001-architecture-hexagonale.md) | Module Go unique, domaine et ports/adaptateurs | Proposé |
| [0002](0002-livraison-durable.md) | Intention durable, destination et migration | Proposé ; transaction v8 implémentée dans la proposition |
| [0003](0003-version-et-artefacts.md) | Version commune injectée et provenance des artefacts | Proposé |
| [0004](0004-validite-et-disponibilite-des-mesures.md) | Quantités, absence et dénominateurs | Proposé |
| [0005](0005-identite-et-provenance.md) | Compte, profil, destination et authenticité | Proposé |
| [0006](0006-temps-actif-et-frontieres.md) | Temps actif, pauses et fusion bornée | Proposé |
| [0007](0007-lecture-des-imports-non-fiables.md) | Lecture seule des imports non fiables | Proposé |
| [0008](0008-transactions-de-connexion.md) | Cookies et transactions de connexion | Proposé |

Utiliser le [modèle](template.md) et le prochain numéro libre. Un ADR traite une décision, pas un epic, une tâche ou un journal de travail. Son statut peut être **Proposé**, **Accepté**, **Rejeté** ou **Remplacé par ADR-NNNN**. Une acceptation relève des mainteneurs ; elle ne démontre pas que le code est implémenté ou publié. Consigner séparément les références de mise en œuvre et de vérification.

Conserver le contexte historique d’un ADR accepté. Une décision remplacée pointe vers celle qui la remplace ; les guides courants sont mis à jour avec le comportement livré. Les liens des ADR proposés vers la révision auditée identifient leur point de départ et ne prétendent pas décrire la dernière version.

Le [dossier d’implémentation](../implementation/README.md) est temporaire : contrats, séquencement et preuves de tâche y vivent pendant les travaux, ainsi que dans l’[issue #9](https://github.com/KcivazB/rocket-tracker/issues/9) et les PR. Avant de retirer une fiche, transférer toute décision durable dans un ADR, tout contrat livré dans le bon guide et toute preuve dans une PR/issue accessible. Le retrait ne clôt pas les tâches encore ouvertes.
