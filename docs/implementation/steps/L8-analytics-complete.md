# L8 — Analytique web, profils et livraison locale

Statut : **implémenté et vérifié localement, à relire indépendamment**. Aucun commit, push, PR, release ou parcours réel n'est prouvé par cette fiche. Révision de départ de cette lane : `17c9fe71df7ac9a1d8d7ffda4d205e02c263bf87` ; les changements des autres owners du checkout partagé sont conservés.

## Contrat et surface

Les trois fichiers de la première tranche L8 ont été repris, puis complétés exclusivement dans [app.js](../../../web/app.js), [i18n.js](../../../web/i18n.js), [analytics.test.js](../../../web/tests/analytics.test.js), [profiles.test.js](../../../web/tests/profiles.test.js) et [polling.test.js](../../../web/tests/polling.test.js). Aucun framework, bundler ou dépendance ajouté. Cette fiche est la seule documentation éditée par cette lane ; les guides partagés restent au coordinateur.

| Cas | Comportement local et oracle |
| --- | --- |
| M01 — médiane | Le partage récent utilise le même prédicat strict ou inclusif que les groupes comparés. Régression avec égalités à la médiane. |
| M02 — temps en l'air | La direction du changement reste descriptive ; une hausse de 60 points n'est pas colorée comme une amélioration par définition. Valeurs conservées, `higherBetter` neutre. |
| M04 — vitesses | Aucune conversion ni détection par amplitude dans le front. Unité explicitement déclarée `uu/s` ou `km/h` : valeur inchangée et unité conservée. Métadonnée absente/vide : cohorte historique, libellé « unités historiques (source non qualifiée) ». Métadonnée `unknown` : cohorte brute distincte. Pas d'agrégation entre ces cohortes ni avec les unités explicites ; autres statistiques préservées. |
| M05 — chronologie | Seule la population de comparaison live exige buts d'équipe connue, temps finis dans la durée et décompte conforme au score. Les statistiques de résultats conservent ces matchs. |
| M06 — moyenne | Libellés explicites « moyenne par match », sans pondération par durée. Deux matchs observés 300 s et 1 s, à 10 % et 90 %, donnent toujours 50 %. |
| M07 — reprise | Un but terminal à 300 s ne crée pas une reprise supplémentaire ; l'engagement initial de prolongation reste compté séparément. |
| M08 — rang | Destination résolue avant préremplissage et conservée jusqu'à l'envoi. Groupes, deltas, courbes et notes séparés par compte et playlist. Anciennes notes sans compte affichées comme non attribuées dans « tous », sans rattachement au compte principal. |
| M09 — session | Construction sur tous les matchs du compte, puis sélection des playlists classées non abandonnées à noter. Occasionnels et abandons prolongent la session ; l'activité d'un autre compte ne la prolonge pas. Seuil de fin 30 min, horizon 12 h inchangés. |
| M10 — échauffement | Aide FR/EN : au moins 5 sessions et 10 matchs décidés parmi les trois premiers, dans chaque groupe. Avec 5 sessions d'un match de chaque côté, afficher 5/5 sessions et 5/10 matchs ; ne pas annoncer une comparaison disponible. |

Les vues « tous » utilisent le compte principal pour la nouvelle saisie et le rappel de rang, avec compte et identifiant affichés. Le formulaire ouvert conserve ce choix même si le filtre change. Un changement de joueur/génération interdit l'envoi de l'ancien formulaire ; une réponse tardive ne contamine pas la nouvelle vue ni un formulaire plus récent. Les rangs restent déclaratifs, sans attestation de plateforme ajoutée.

### M04 : portée de la précaution

Le contrat parallèle `speed_unit` est pris en compte sans toucher aux modèles ou au tracker. Le front conserve les nombres reçus et transmet l'unité du live aux widgets. La convention historique `uu/s` documentée dans `internal/domain/speed.go` ne prouve pas la provenance physique : les lignes sans métadonnée restent dans une cohorte `legacy` interne au front, séparée des nouvelles captures `unknown` et des unités déclarées. Aucun nouveau champ n'est envoyé à l'API. Les comparaisons match/moyenne vérifient aussi la compatibilité de la référence ; mécanique, fatigue, facteurs et gain de frappe ne combinent pas ces cohortes.

Le taux « frappes puissantes » est affiché indisponible tant que l'unité source n'est pas qualifiée ; le conseil de drill n'impose plus un objectif de vitesse chiffré. Le compteur historique interne de touches au-dessus du seuil brut est conservé pour les consommateurs analytiques existants, sans restitution comme seuil physique. Les métriques de position et les autres seuils ne sont pas requalifiés par cette lane.

Une étiquette d'unité compatible ne prouve ni une capture réelle ni la comparabilité de sources anciennes inconnues. La normalisation physique et la qualification du flux restent un gate distinct, sans fixture de jeu réel ici. Pas de migration ou réécriture des valeurs stockées.

### UI01–03 : révision, retry et réponses tardives

Correction additionnelle sur base `33674b3154e73454d5b6c080deba4693e8549cf4`. Les rangs et les saisies manuelles ont chacun leur marqueur de succès, état occupé et échec. Un polling retente les rangs échoués même si les matchs ont déjà été chargés à cette révision ; un changement de `data_rev` recharge aussi les saisies manuelles, même à nombre de matchs inchangé. Un échec conserve les dernières valeurs reçues. Une requête n'acquitte que la révision observée à son départ ; une nouvelle révision pendant son exécution est reprise au polling suivant, sans lancer de doublon de polling pendant qu'elle tourne. Le fallback historique par compteurs ne peut pas détecter une édition manuelle sans changement de compteur : il faut `data_rev` pour cette garantie.

Les réponses restent gardées par génération de joueur ; le changement de route efface aussi les rangs et partages de la vue précédente. La continuation config/manual du chargement initial vérifie la génération avant de lancer les matchs. Une mise à jour des matchs du même joueur conserve le profil de jeu choisi pendant la requête. Le garde de séquence déjà présent du classement est conservé et désormais exercé avec réponses dans l'ordre inverse.

### Statut local de livraison

Le champ local `/api/status.delivery` expose `pending`, `held`, `rejected`, `legacy_held`. Le bloc d'avertissements existant présente les quatre compteurs FR/EN et distingue sauvegarde sur le PC et réception par le serveur. En attente : vérifier agent/connexion ; blocages : vérifier serveur et compte prévus avant récupération/import explicite ; refus : examiner avant correction et nouvel import.

Aucun bouton, transport, réaffectation ou envoi automatique n'est ajouté. `index.html` reste intact. Le bloc n'apparaît pas en mode serveur, pour un autre joueur, si le statut est périmé, ou si les compteurs sont incomplets/invalides. Une file à zéro ne prétend pas prouver la livraison de tout l'historique.

## Preuves exécutées

Hôte macOS arm64, Node `v26.11.0`, Go `go1.27.2`. Depuis la racine :

```sh
node web/tests/analytics.test.js
node web/tests/i18n.test.js
node web/tests/profiles.test.js
node web/tests/polling.test.js
go test ./web -count=1 -v
go vet ./web
```

**PASS** pour les six commandes. Traductions : 949 clés, 887 utilisées. Après extension explicite de l'ownership à `web/web_test.go`, le wrapper Go lance les quatre scripts séparément, chacun une seule fois. `polling.test.js` est autonome : aucun appel imbriqué depuis `profiles.test.js` ou `analytics.test.js`. Son refus HTTP est également testé dans le wrapper. Aucune modification des workflows ou de l'embed par cette lane. Commande supplémentaire à intégrer dans les invocations Node explicites de CI : `node web/tests/polling.test.js` (coordinateur/Copernicus).

`TestEmbeddedAssets` vérifie les sept assets runtime et les refus HTTP en mémoire, dont `/tests/profiles.test.js` et `/tests/polling.test.js`. Aucune écoute réseau réelle. Les vérifications Node utilisent les fonctions originales de la fermeture UI dans une VM, un formulaire/DOM minimal, des configurations Chart capturées et un faux `fetch` local. Elles vérifient préremplissage, corps des requêtes, changement de profil/route, réponse tardive, séparation des courbes, sessions et textes de livraison ; elles n'exécutent pas un navigateur, le rendu Chart ou un serveur externe.

Échecs observés avant corrections de cette lane : libellé M06 absent ; M08 une synthèse au lieu de trois comptes ; M09 rappel incorrect après activité occasionnelle ; M04 52,2 au lieu de la valeur reçue 1450 ; statut de livraison absent. Les mêmes régressions passent après correction. Les cinq cas repris de la première tranche ont été réexécutés ; leur état rouge n'a pas été recréé dans ce checkout.

Les régressions additionnelles couvrent les valeurs déclarées `uu/s`/`km/h` sans conversion et leur non-agrégation avec une unité différente, les rendus réels de buts en FR/EN, ainsi que les deltas MMR par compte et playlist. Les assertions de traduction et d'analytique existantes sont conservées.

Sur la base `33674b3`, trois échecs supplémentaires ont été observés avant correction : moyenne de vitesse legacy/unknown à **780** (1500 et 60) au lieu d'indisponible ; **1** tentative de rang au lieu de **2** après un échec puis polling à révision inchangée ; saisie manuelle à **1** match au lieu de **2** après `data_rev` 7 → 8. Ces assertions passent après correction, sans conversion. Les rendus FR/EN des buts distinguent les deux cohortes ; une moyenne historique n'est pas affichée comme référence d'un match `unknown`.

`polling.test.js` exerce les loaders et `pollStatus` originaux avec huit scénarios : retry rang avec `data_rev`, retry avec compteurs historiques, invalidation/retry manuel, révision pendant requêtes en attente, classement avec ancien filtre résolu en dernier, profil de jeu changé pendant rafraîchissement, ancien joueur résolu après nouvelle route, ancien chargement initial résolu en dernier. Les promesses sont contrôlées sans sommeil ; un timeout ne sert qu'à faire échouer un test bloqué. Seules les fonctions de présentation sont neutralisées dans cette VM ; aucun intervalle de navigateur, DOM complet, appel HTTP réel ou rendu visuel n'est prouvé par ces huit scénarios.

## Revue analytique et limites

| Perspective | Conclusion locale | Objection la plus forte et limite |
| --- | --- | --- |
| Principal Engineer | Invariants couverts par les tests ciblés, sans nouveau cadre technique. | La VM expose des fonctions de fermeture : elle est liée à la structure du fichier et ne remplace pas un test de navigateur. Le contrat d'unité reste distinct d'une preuve de provenance. |
| Product Engineer | Comptes, populations, moyennes et états de file sont explicites en FR/EN. | Les anciennes notes non attribuées restent visibles dans « tous » et ne sont pas automatiquement retrouvées sous un compte. Les libellés longs et les courbes multiples nécessitent encore une revue visuelle. |
| SRE | Le statut de file ne promet pas une réception distante ; vues serveur/autre joueur et données périmées sont exclues. | Aucun envoi/récupération réel, navigateur Windows ou jeu exécuté. Les compteurs dépendent de l'API locale intégrée par le coordinateur. |

M03, stockage, tracker, ports, pipeline et guides partagés restent hors ownership de cette lane. La review indépendante du coordinateur et la limite de revue sous modèle hérité restent ouvertes ; aucune diversité de fournisseurs n'est revendiquée. Les fichiers sont remis stables pour contrelecture et intégration ; aucune publication effectuée.

## Intégration upstream `f936e58`

Ajouts conservés : événements et momentum, duels/filtres, forfaits et Wilson,
analyses mur/mode et facteurs stratifiés. Les assertions des deux côtés du merge
sont présentes. Cas croisés ajoutés : engagement terminal à 180/300 s, événements
absents versus vides, tendances/séries avec cohortes différentes, split car null
exclu. Les quatre scripts Node passent (977 clés, 908 utilisées).
