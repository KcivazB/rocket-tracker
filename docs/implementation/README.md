# Plan d’implémentation

Suite de l’audit : [plan de correction des 22 findings de l’issue #9](ISSUE-9-PLAN.md) et [matrice de vérification](ISSUE-9-VERIFICATION.md).

18 octobre 2026. Proposition fondée sur `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3`, à revalider au départ de chaque lot. Ce plan ordonne les travaux du rapport ; sa rédaction seule ne constitue pas une implémentation. Les statuts de préparation se rapportent à la révision de référence ; les preuves d’avancement sont portées par les PR et les fiches mises à jour. Il ne crée pas une seconde roadmap acceptée.

[Audit consolidé et plan publié](https://github.com/KcivazB/rocket-tracker/issues/2#issuecomment-6250332932) · [Architecture actuelle](../ARCHITECTURE.md) · [Décisions durables — ADR](../decisions/README.md)

**Perspective proposée : corriger d’abord les invariants démontrés, puis extraire l’architecture par parcours complet.** Garder un module Go, SQLite, le dashboard embarqué et les deux exécutables actuels. Les correctifs et les décisions produit ne doivent pas attendre le refactor complet.

## État de la proposition complète

Voir [la requalification](REQUALIFICATION.md) et [les preuves finales](VERIFICATION.md).
Les lots L0–L10 disposent de leur implémentation et de leur fiche de livraison ;
les qualifications externes et l’adoption des hypothèses produit sont séparées.
Une PR proposée et testée n’est pas une intégration, release ou déploiement.
Les sections de préparation ci-dessous conservent les critères initiaux.

## Durée de vie des documents

Ce dossier est **temporaire** : il prépare et accompagne les travaux. Le code produit est destiné à durer. Les [ADR](../decisions/README.md) conservent les décisions durables ; les guides décrivent les contrats effectivement implémentés ; l’[issue et son plan](https://github.com/KcivazB/rocket-tracker/issues/2#issuecomment-6250332932) ainsi que les PR conservent le suivi et les preuves.

Retirer une fiche seulement après avoir transféré ses éléments utiles : décision et compromis vers un ADR, comportement et procédures vers le guide concerné, résultats et limites vers la PR/issue. Vérifier les liens entrants avant retrait. Conserver les questions ouvertes et les tâches restantes dans le suivi ; supprimer une fiche ne clôt pas son lot. Aucun retrait n’est déclenché par la seule création des ADR.

Les personas, epics et user stories restent dans ce dossier et le suivi pendant leur validation. Une fois confirmés, leurs besoins et parcours durables vont dans la documentation produit ; leur statut de livraison reste dans les issues/PR, pas dans les ADR.

## Socle documentaire : personas, epics et user stories

Les personas suivants sont des **hypothèses de besoins dérivées des parcours du code et de l’audit**, à confirmer avec les mainteneurs/utilisateurs. Ce ne sont pas des entretiens réalisés ni des catégories exclusives : une personne peut cumuler plusieurs besoins.

| Persona | Besoin et difficulté à traiter | Stories concernées |
|---|---|---|
| P1 — Joueur local/offline | Installer une fois, enregistrer la bonne partie, conserver son historique sans serveur et comprendre les pannes. | US-01, 02, 04, 07, 09 |
| P2 — Joueur multi-profils ou multi-postes | Ne pas mélanger ses comptes, noter le bon rang et retrouver son historique à la bonne destination. | US-02, 03, 05, 08, 09 |
| P3 — Joueur qui analyse et s’entraîne | Comparer une population explicite, comprendre les limites des conseils et conserver ses éditions. | US-03, 04, 06, 07, 10 |
| P4 — Opérateur d’un serveur auto-hébergé | Isoler les comptes, maîtriser ingestion/ressources, comprendre la provenance et restaurer sans perdre ni réaffecter les données. | US-08, 09, 10 et US-11 proposée ci-dessous |

Réutiliser **US-01 à US-10 du rapport**, avec leurs identifiants et critères, plutôt que créer un backlog concurrent. Pour chaque story retenue : persona, problème, déclencheur, préconditions, parcours nominal, alternatives/pannes, invariant, scénario Given/When/Then, source/test et statut de preuve. Les critères proposés doivent être convenus avant d’être présentés comme acceptés.

**US-11 proposée — Restaurer une instance :** « En tant qu’opérateur, je veux restaurer un jeu cohérent de données, configuration et intentions pour retrouver le service sans perte ni envoi au mauvais compte. » Given un backup synthétique cohérent et des livraisons en attente, When la restauration et la reprise se terminent en isolation, Then les résultats/éditions sont préservés, les rejoués convergent et les destinations ambiguës restent à l’écart. Définir fréquence de sauvegarde et objectifs de récupération selon le besoin ; aucun RPO/RTO mesuré n’est annoncé.

| Epic proposé | Résultat attendu | Stories / lots | Preuve de clôture |
|---|---|---|---|
| E0 — Contrats et preuve | Vocabulaire, personas, état actuel et cible compréhensibles. | Toutes / L0 | Matrice constat→story→lot→test, décisions ouvertes nommées. |
| E1 — Ingestion fiable | Une entrée invalide n’altère pas l’historique. | US-02, 09 / L1, L2 | Refus sans effet, compatibilité, scope et rejouabilité testés. |
| E2 — Frontend vérifiable | Tests au bon endroit, assets strictement applicatifs. | US-03 à 07 / L3 | Assertions conservées, scripts explicites, absence de tests servis. |
| E3 — Architecture hexagonale | Domaine et orchestration séparés des effets, comportement conservé. | US-01, 02, 03, 06, 08, 09 / L4, L5, L6 | Imports, doubles, séquences et parcours Windows/serveur vérifiés. |
| E4 — Livraison récupérable | Un résultat enregistré et destiné au serveur reste livrable. | US-08, 09 / L7 | Interruptions, migration, destination et acquittements testés. |
| E5 — Mesures et états honnêtes | Bon profil, population et information de reprise explicites. | US-01, 03 à 07, 10 / L8 | Fixtures métier et critères UX du périmètre livré. |
| E6 — Livraison et exploitation | Version, candidat de release et restauration traçables. | Transverse, US-11 / L9, L10 | Builds/tests, identité des octets et restauration isolée prouvés. |

Les epics sont une proposition de regroupement et ne sont pas des issues supplémentaires créées. Chaque fiche d’epic doit nommer responsable, bénéfice, périmètre/hors périmètre, stories, lots, dépendances, risques, décision ouverte et démonstration de fin. Les mainteneurs valident les priorités et le découpage.

## Documentation d’architecture et rôles de revue

Le document d’architecture doit couvrir quatre vues, avec **actuel** et **cible proposée** séparés : contexte (joueur, jeu, appareil, serveur et services), conteneurs/composants (deux exécutables, DB, web, ports/adaptateurs), séquences (capture, sauvegarde, upload/import, erreur/rejeu/reprise), et déploiement/confiance (Windows, serveur, authentification, données et destinations). Le rapport et son document d’architecture hexagonale fournissent le point de départ, pas la preuve d’une migration réalisée.

Les décisions qui changent un contrat ont leur fiche : question, sources/révision, options, décisionnaire, choix/statut, compatibilité/migration, objections, critères de réexamen. En particulier : garantie durable/destination, version, interprétation analytique et visibilité/attribution. Les [ADR dans `docs/decisions/`](../decisions/README.md) conservent ces décisions, leurs alternatives et leur statut ; les premières propositions ne sont pas encore des décisions acceptées.

Les **rôles de revue** sont distincts des personas utilisateurs : architecture/dépendances (Principal Engineer), valeur/parcours/oracle (Product Engineer), reprise/builds/exploitation (SRE), et frontière d’autorisation/intégrité (sécurité pour L1/L2/L5/L7/L10). Ce sont des responsabilités à attribuer, pas des personnes déjà consultées ou des validations acquises. Un rôle non applicable doit être justifié.

## Une documentation pour chaque étape

Chaque lot possède sa **fiche locale de préparation** dans `steps/`. Elle décrit le contrat avant code, les documents à maintenir pendant l’implémentation et les preuves à joindre après vérification. Les documents produit futurs ci-dessous sont des livrables attendus ; leur présence dans le plan ne prétend pas qu’ils existent aujourd’hui.

| Étape / fiche | Avant de coder | Dans la même PR que le comportement | Preuve documentaire de fin |
|---|---|---|---|
| L0 — `L0-cadrage.md` | Personas, epics, US et matrice actuelle/cible ; choix du périmètre. | Corriger uniquement les instructions courantes déjà vérifiées ; consigner la cible dans le dossier de conception. | Révision, statuts des constats, scénarios retenus, décisions/risques/responsables ouverts. |
| L1 — `L1-quantites.md` | Contrat des quantités, absence/zéro/signé et chemins upload/import. | `docs/SPEC.md` : valeurs acceptées, rejet et compatibilité ; guide d’ingestion si l’utilisateur doit agir. | Cas refusés sans effet, cas historiques acceptés et résultats exacts. |
| L2 — `L2-enveloppe-json.md` | Contrat de corps, borne, EOF et statuts d’erreur. | `docs/SPEC.md` et section ingestion de `docs/SELF_HOSTING.md` : format et limites effectives. | Seuils, suffixes, longueur inconnue et absence d’écriture vérifiés. |
| L3 — `L3-tests-web.md` | Inventaire des assertions et des assets embarqués/servis. | `docs/DEVELOPMENT.md`, table de routage `AGENTS.md`, commandes de test/CI ; architecture web si nécessaire. | Liste des scripts exécutés et vérification d’assets sans tests/fixtures. |
| L4 — `L4-domaine.md` | Diagramme de dépendances de la tranche, glossaire et mapping d’observations. | `docs/ARCHITECTURE.md`, `docs/SPEC.md` et routage agent : frontières réellement extraites. | Imports et cas d’absence/unité/identité, couplages restants explicites. |
| L5 — `L5-cas-usage.md` | Séquences locales/serveur, ports, erreurs et scope autorisé. | `docs/ARCHITECTURE.md` et contrats d’ingestion : orchestration effective, limites DB/file. | Tests local/upload/import, pannes, éditions, convergence et absence de boucle. |
| L6 — `L6-composition.md` | Schéma de lifecycle et inventaire CLI/autostart/version/données. | `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md` ; README seulement si le parcours d’usage change. | Cibles compilées et résultats Windows natifs, arrêt/restart/autostart. |
| L7 — `L7-livraison-durable.md` | Décision de garantie et plan de migration avec destination ambiguë. | `docs/ARCHITECTURE.md`, `docs/SELF_HOSTING.md`, aide de reprise/import ; nouvelles migrations expliquées. | Interruption/reprise/migration rejouable, acquittement de révision, sources conservées et limites de rétrogradation. |
| L8 — `L8-metier-ux.md` | Story et fixture par règle, contrat de mesure et maquette des états utiles. | `docs/SPEC.md`, aide FR/EN, parcours d’identité/partage ; décision conservée séparément. | Résultats métier, messages/états et environnements de rendu réellement vérifiés. |
| L9 — `L9-version-ci.md` | Décision de version, matrice des consommateurs et schéma Build/Tests/Release. | `docs/DEVELOPMENT.md`, règle de version `AGENTS.md`, mode d’update et procédures de publication concernés. | Valeurs simple/injectée, checks et identité du candidat/artefact ; aucune release supposée. |
| L10 — `L10-exploitation.md` | Contrats de ressources, inventaire de backup et scénario de restauration. | `docs/SELF_HOSTING.md` : limites effectives, sauvegarde, restauration, erreurs et reprise ; guides liés. | Runbook rejoué sur données synthétiques, écarts et environnements non qualifiés. |

Chaque fiche d’étape contient : epic et stories, révision/source, état **proposé**, responsable/réviseur à attribuer, dépendances réelles, objectif/hors périmètre, contrat, scénarios, documentation avant/pendant/après, risques/migration, critères de passage et prochaine action. Une fiche évolue avec les preuves ; une case ne devient pas « terminée » par la seule création du document.

**Passage documentaire obligatoire :** conception et oracle convenus avant le code ; documentation courante modifiée dans la PR qui rend le comportement effectif ; commandes/liens/exemples vérifiés et preuve de test jointe avant de déclarer le lot prêt à intégrer. Conserver dans les décisions la justification, pas un historique ajouté au README. `CLAUDE.md` continue à importer `AGENTS.md` ; ne pas dupliquer les règles.

## Fiches par étape

- [L0-cadrage](steps/L0-cadrage.md)
- [L1-quantites](steps/L1-quantites.md)
- [L2-enveloppe-json](steps/L2-enveloppe-json.md)
- [L3-tests-web](steps/L3-tests-web.md)
- [L4-domaine](steps/L4-domaine.md)
- [L5-cas-usage](steps/L5-cas-usage.md)
- [L6-composition](steps/L6-composition.md)
- [L7-livraison-durable](steps/L7-livraison-durable.md)
- [L8-metier-ux](steps/L8-metier-ux.md)
- [L9-version-ci](steps/L9-version-ci.md)
- [L10-exploitation](steps/L10-exploitation.md)

## Ordre et répartition

Le responsable de chaque PR implémente et fournit les preuves ; l’autre développeur relit scénario, oracle et compatibilité avant intégration. Ces rôles sont à attribuer au démarrage, sans supposer de disponibilité ni de délai.

| Lot | Dépendances réelles | Livrable |
|---|---|---|
| L0 — Requalification | Aucune | Matrice actuelle des constats et scénarios de référence. |
| L1 — Quantités S1 | L0 pour le contrat concerné | Validation partagée, refus sans persistance. |
| L2 — Corps JSON S2 | L0 pour le contrat concerné | Enveloppe bornée et une seule valeur avant écriture. |
| L3 — Tests web | L0 pour les commandes/fixtures | Tests traduction et analytique séparés, exclus des assets. |
| L4 — Domaine | L0 ; intégrer le contrat L1 pour la validation extraite | Types et observations sans imports d’infrastructure. |
| L5 — Application | L4 | Enregistrement local et ingestion serveur distincts. |
| L6 — Composition Windows | Contrat L0 ; intégration coordonnée avec L5 | Entrée mince et lifecycle conservé ; inversion achevée seulement après L4/L5. |
| L7 — Livraison durable | Contrat identité/destination requalifié par L0 ; décision de garantie | Résultat et intention récupérables, reprise démontrée. |
| L8 — Règles métier et UX | L0 ; décisions propres à chaque règle | Petits correctifs des cas encore reproduits. |
| L9 — Version et CI | L0 ; convention de version adoptée | Version cohérente et candidat de release traçable. |
| L10 — Exploitation et qualification | Contrats du périmètre livré ; L7 pour qualifier la nouvelle reprise | Budgets choisis, restauration et vérification des parcours ciblés. |

Après L0, lancer L1/L2 en séquence s’ils touchent le même handler. L3 peut avancer en parallèle des correctifs Go. L8 partage potentiellement `web/app.js` avec L3 : sérialiser ce fichier et le script de tests lors de l’intégration. L9 partage workflows/docs avec L3 et entrées avec L6 : un seul rédacteur par surface, intégrations ordonnées. L7 peut être conçu et corrigé sur les adaptateurs existants sans attendre L4–L6 ; ne pas mener simultanément deux écritures de l’orchestration L5/L7.

## L0 — Établir la référence exécutable

- Relever HEAD, état du checkout et CI pertinente ; relire les tests actuels et les guides.
- Requalifier DATA-01 à 06, UI-01 à 03, SEC-01 à 03 et M-01 à 10 : corrigé avec preuve, reproduit, non reproduit ou décision ouverte. Les deux S1/S2 sont prouvés uniquement à la révision de l’audit tant que non rejoués.
- Transformer les probes nécessaires en tests maintenus qui affirment le comportement voulu. Vérifier l’échec attendu sur la tête retenue avant le correctif ; ne pas intégrer une assertion qui fige le défaut.
- Capturer des fixtures synthétiques offline, partiel, abandon→final, deux profils, import/rejeu et réponse tardive. La qualification exhaustive ne bloque pas le premier correctif dont le contrat est prêt.

**Fin :** chaque correction engagée possède son scénario, son propriétaire de règle et un résultat attendu discriminant. Aucun verdict ancien n’est repris sans vérification.

## L1/L2 — Corriger les frontières d’ingestion

**L1, surfaces :** validateur dans `internal/server/hub.go`, chemins d’import et validation/store concernés. Extraire seulement l’aide pure nécessaire pour réutiliser les contraintes entre upload et import ; pas de préalable architectural global. Refuser durées et compteurs impossibles, conserver champs historiques absents et grandeurs légitimement signées. Définir la frontière de rejet d’un import avant de modifier son atomicité globale.

**Vérification L1 :** requête authentifiée invalide refusée, relecture DB sans nouvel effet ; valeur zéro explicite, partie partielle, ancien format et position signée acceptés. Conserver scope, idempotence et remplacement abandon→final. Un score plausible ne devient pas une donnée attestée.

**L2, surface :** lecture HTTP du payload avant appel à l’ingestion. Lire et contrôler l’enveloppe entière dans le budget, exactement une valeur JSON ; ne pas compter seulement sur `Content-Length` ou sur un limiteur jamais consommé jusqu’au contrôle de fin.

**Vérification L2 :** objet valide, espaces dans la borne, seconde valeur, suffixe invalide, limite exacte et dépassement ; même vérification avec longueur inconnue ; erreur de format distincte du dépassement, zéro écriture en cas de rejet. Les tests en mémoire ne qualifient pas le proxy ni une résistance réseau.

## L3 — Rapprocher les tests du frontend

Séparer `devtools/i18n_test.js` en `web/tests/i18n.test.js` et `web/tests/analytics.test.js`. Conserver `web/i18n.js` comme traduction runtime et les exports analytiques de `web/app.js`. Adapter `require`, `__dirname`, wrapper Go, commandes CI et guides ensemble.

**Fin :** toutes les assertions existantes sont conservées et les deux scripts sont exécutés explicitement avec Node. Borner les patterns de `web/embed.go` : `//go:embed *` inclurait le sous-dossier. Vérifier la liste des fichiers embarqués, l’absence des tests/fixtures dans les assets servis et la présence de chaque asset nécessaire. Pas de bundler supplémentaire.

## L4/L5/L6 — Migrer vers l’architecture hexagonale

**L4, première tranche :** extraire les types purs utiles de `internal/store/model.go` et le mapping `statsapi.Event`→observations métier. Faire suivre les règles tracker/tilt concernées. Préserver absence des champs, unités, identité, horloge et tolérance de décodage. Éviter une duplication de tous les DTO.

**Fin L4 :** domaine et tracker n’importent plus store/SQL, HTTP, Windows ou DTO Stats API dans la tranche déclarée. Vérifier les imports et les tests des invariants ; déclarer les couplages restants au lieu d’annoncer le domaine entier pur.

**L5 :** introduire les cas locaux `RecordResult`/`RecordTraining` et serveur `IngestResult`/`IngestTraining`, puis leurs seuls ports nécessaires. Réutiliser les adaptateurs et fonctions injectées existants. Authentification et budget HTTP restent à la frontière ; le contexte propriétaire autorisé est explicite. Aucune ingestion serveur ne remet un élément dans la file d’upload.

**Fin L5 :** doubles de stockage/file pour absence de serveur, agent arrêté, échec DB, checkpoint et échec de file. Côté serveur, réutiliser les tests upload et import : propriétaire imposé hors payload, refus sans effet à la frontière choisie, profils distincts, rejeu et abandon→final, éditions conservées selon le contrat. Préserver ordre et résultats actuels ; l’extraction seule ne promet pas une sauvegarde atomique ni un retry du callback tracker. Pas de cycle tracker/application/agent et pas de bus générique.

**L6 :** amincir `main.go`, `desktop.go`, `wizard.go` et `agent_cmd.go` au profit d’une composition `desktopapp` compatible avec la sélection de plateforme. Cette extraction mécanique peut précéder L5 en conservant les callbacks ; coordonner les fichiers partagés et n’annoncer l’inversion achevée qu’après L4/L5. Garder l’entrée racine, la capacité d’injection de version (voir [ADR-0003](../decisions/0003-version-et-artefacts.md)), noms/arguments/autostart et chemins de données. Le déplacement de `build.ps1` reste optionnel et séparé.

**Fin L6 :** builds Windows et serveur Linux, tests Windows natifs, puis arrêt/restart et autostart qualifiés en environnement Windows isolé. Arrêter la capture avant flush et fermeture, libérer les ressources avant restart. Un cross-build ne valide pas ce lifecycle.

## L7 — Fermer la fenêtre DB/file

Décider de la garantie « résultat sauvegardé et serveur configuré ⇒ intention d’envoi récupérable ». Comparer transaction SQLite résultat+outbox et marqueur durable réconciliable ; retenir une option. Définir destination serveur+compte, identité locale et révision de livraison ; le token rotatif n’est pas l’identité de destination.

**Livrable :** migration ordonnée et reprise des fichiers existants, traitement explicite des rejets et de l’ancien historique, statut actionnable si la garantie ne peut être tenue. La file actuelle trace le serveur, sans propriétaire par entrée : le compte du jeton courant ne prouve pas la destination historique. En cas d’ambiguïté, conserver à l’écart sans envoi automatique ; décider séparément du rattachement. Inclure files rejetées et déjà mises à l’écart, rendre la migration rejouable après interruption et conserver les sources jusqu’au transfert durable vérifié. Ne pas réécrire les migrations appliquées ni vider les files pour simplifier la migration.

**Fin :** tests d’interruption avant/après commit et acquittement, redémarrage, agent arrêté, rejeu, changement serveur/compte, rotation de token et acquittement tardif d’une ancienne révision. Ni perte ni envoi à une destination différente de celle autorisée ; une ancienne réponse n’efface pas la nouvelle révision. Fixtures temporaires uniquement. Documenter backup et récupération ; une rétrogradation vers une version qui ignore l’outbox SQLite ne doit pas être annoncée sûre sans mécanisme vérifié.

## L8 — Corriger l’analytique et rendre l’état compréhensible

Une PR par invariant encore démontré. Ordre proposé : disponibilité/identité (M-03, M-08), éligibilité/chronologie (M-05, M-07, M-09), puis prédicats, interprétation et aide (M-01, M-02, M-06, M-10). M-04 nécessite d’abord une capture à unité connue : ne pas convertir sur une hypothèse non qualifiée.

**Fin :** fixtures du rapport et contre-exemples voisins passent, population/unité/effectif/dénominateur sont explicites ; profil destinataire choisi avant préremplissage ; texte FR/EN cohérent. Qualifier séparément rendu DOM, notifications et jeu réel si touchés. Les correctifs indépendants ne dépendent pas du refactor hexagonal.

Pour l’UX, distinguer absent/connecté/en attente/réparation et l’état de sauvegarde locale de la livraison. Distinguer expéditeur, profil déclaré et compte lié ; ne pas présenter la partie comme attestée. Les mainteneurs décident visibilité/classement/consentement avant toute intégration plateforme ; cette intégration est conditionnelle, sans bloquer les libellés honnêtes.

## L9/L10 — Rendre la livraison et la récupération vérifiables

**L9 :** appliquer la convention de l’[ADR-0003](../decisions/0003-version-et-artefacts.md) : une variable commune `buildinfo.Version`, fallback `dev`, injection au build et suppression des bumps de sources. Migrer ensemble scripts, Docker, workflows et guides vers le nouveau symbole du linker. Vérifier la valeur annoncée par les binaires et le comportement updater.

**Fin L9 :** les deux exécutables répondent correctement en build simple et injecté, tag normalisé et updater compris. Les gates Build et Tests couvrent les plateformes et Node explicitement. Le candidat de release est identifié et vérifié avant publication ; la release autorisée consomme ces mêmes octets. Une publication de branche Docker et une release tag ont des preuves distinctes.

**L10 :** choisir et documenter tailles/durées/concurrence/temporaire et réactions de rejet avant d’ajouter des quotas. Tester avec lecteurs/horloges/effets factices ; toute mesure de charge réelle nécessite un environnement autorisé séparé. Restaurer un jeu synthétique DB/WAL, configuration et file avec leurs identités et intentions de livraison ; ne pas se limiter à une copie du fichier DB vivant.

**Fin L10 :** procédure de restauration rejouée en isolation, reprise des intentions et absence de doublon vérifiées ; démarrage sans secrets ou données de joueur ; parcours offline puis reconnexion démontré. Annoncer séparément ce qui reste non qualifié : jeu Windows réel, IdP, proxy, navigateur, Docker ou plateforme.

## Contrat de chaque PR et conditions de passage

1. Révision de départ et constat requalifié ; invariant et compatibilité explicites.
2. Plus petit changement cohérent, scénario discriminant puis vérifications des cas voisins. Pour un déplacement pur, comparer comportement et assets ; ne pas inventer un test rouge artificiel.
3. Tests ciblés et checks applicables du guide de développement, avec résultat exact et limites de plateforme ; fiche d’étape, documentation courante et décisions concernées mises à jour dans la même PR.
4. Revue : architecture et dépendances, impact joueur, reprise et exploitation. Résoudre l’objection la plus forte avec preuve ou décision nommée.
5. Preuves de revue/intégration séparées des preuves de release/déploiement. La rédaction de ce plan ne lance pas les lots et n’autorise ni merge ni publication d’artefact.

Pas d’estimation calendaire avant L0 et attribution des responsables. Première tranche livrable : S1/S2 et tests web ; deuxième : règles métier encore démontrées et premières frontières hexagonales ; garantie durable et qualification avancent selon leurs contrats propres.
