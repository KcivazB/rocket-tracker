# Requalification et preuves de la proposition complète

Référence amont vérifiée : `6f531fd43be3bcd6f3a746e3b352103ef2a06ef3`.
Amont intégré pendant la livraison : `f936e58` (tirs/arrêts, duels et analyses
par mode conservés). Les verdicts ci-dessous décrivent le code proposé et les tests nommés ; ils ne
signifient pas intégré, publié, déployé ou qualifié sur un vrai jeu.
Les besoins P1–P4, E0–E6 et US-01–11 gardent les identifiants du plan. Aucun
entretien, consentement de partage ou accord de mainteneur n’est inventé.

| Constat | État dans la proposition | Preuve / limite |
| --- | --- | --- |
| DATA-01 destination | Journal vérifié par installation + compte, pas token | `TestJournalOutageRotationAndAccountIsolation`, `TestRecordIntentAtomicAndRevisionAcknowledgement`; legacy inconnu retenu. |
| DATA-02 DB/file | Une transaction résultat + intention | Trigger d’échec du journal : aucune ligne locale partielle ; agent arrêté couvert côté bureau. |
| DATA-03 profils | Rapprochement par propriétaire/GUID/profil conservé | `TestOnlineMatchIdentityIncludesProfile`, `TestUpgradeFillsProfile`; historique sans profil reste ambigu selon contrat existant. |
| DATA-04 concurrence | Lookup et écriture transactionnels conservés | `TestConcurrentIngestSameMatch`; assertions SQL, pas seulement race mémoire. |
| DATA-05 offline/import | Rapprochement date/GUID/profil conservé | `TestOfflineMatchLiveThenImport`, tests d’import et de scope. |
| DATA-06 training | Fusion monotone avec focus édité conservé | `TestTrainingReplayIsIdempotent`, origine persistante v9, filtre de profil sur clé et proximité ; import HTTP à deux profils rejoué. |
| UI-01 invalidation | Révisions suivies par population | `polling.test.js` : rangs, manuel, matchs et entraînements ; pas de nouveau backend de cache. |
| UI-02 retry | Retry indépendant des rangs et saisies manuelles | Huit scénarios de promesses contrôlées ; un échec ne valide pas la population reçue. |
| UI-03 réponses tardives | Générations de route/classement conservées | Tests VM/DOM de changement de profil/joueur et sauvegarde retardée. |
| SEC-01 CSRF local | Guard Host/origine existant conservé | `TestLocalCSRF`, routes concernées. |
| SEC-02 redirect OIDC | `safeNext` existant conservé | `TestSafeNext`; fournisseur IdP réel hors fixture. |
| SEC-03 session glissante | Renouvellement DB/cookie existant conservé | `TestSessionCookieSlides`; aucun déploiement navigateur/IdP revendiqué. |
| S1 quantités | Validation partagée extraite dans application | `ingestion_boundary_test.go` : refus sans effet, zéro/partiel/signé acceptés. |
| S2 enveloppe JSON | Corps complet borné, une valeur | Tests limite exacte/dépassement/suffixe/multiple/longueur inconnue. |
| M-01/02/05/06/07/08/09/10 | Corrections et contre-exemples synthétiques | [Fiche L8](steps/L8-analytics-complete.md), `web/tests/analytics.test.js` et `profiles.test.js`. |
| M-03 disponibilité | Paquet voiture minimal et démolition seule séparés | Tests domaine/tracker/mapping et HTTP : champs car absents/null refusés si temps car positif ; legacy conservé. |
| M-04 unité | Unité déclarée, sinon inconnue ; aucune heuristique de magnitude | Tests unité déclarée ; cohortes legacy/unknown séparées ; aucune capture de jeu réel à unité attestée n’est disponible. |

## Couverture et limites de qualification

- Architecture : tests d’import domaine/tracker, mapping tolérant, ports application,
  scope serveur explicite et aucune boucle d’upload serveur.
- Persistance : rollback journal, rejeu, révision tardive, upgrade v7, réouverture,
  préservation rejouable des anciennes files. Fixtures exclusivement synthétiques.
- Exploitation : [limites et restauration](../OPERATIONS.md), test de restauration
  isolée avec identité et intention ; limites de durée SQLite/disk explicites.
- Livraison : [release](../RELEASING.md), Build/Tests séparés, candidat immuable,
  version `dev` ou injectée ; aucun tag, release, push registre ou deploy exécuté.
- Windows : compilation croisée locale et tests natifs CI sont deux preuves
  distinctes. Tray, élévation, autostart et update interactifs avec un vrai jeu
  demandent une session Windows autorisée. Le plan n’autorise pas leur simulation
  comme preuve réelle.
- Produit : personas et stories sont des hypothèses traçables ; les règles de
  visibilité/classement et toute intégration plateforme requièrent une décision
  distincte. Les télémétries envoyées restent déclaratives, pas attestées.

Le résultat final de chaque commande et la révision figée sont consignés dans
la PR et `VERIFICATION.md`. Cette fiche temporaire se retire après transfert
vers le suivi ; les décisions et contrats durables restent dans ADR/guides.
