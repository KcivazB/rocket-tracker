# ADR-0007 — Lire les imports sans ouvrir le store applicatif

- Statut : **Proposé**
- Contexte : SEC4 de l’issue #9, fichier SQLite contrôlé par un utilisateur.

## Décision proposée

Séparer le lecteur d'import du store applicatif : ouverture en lecture seule,
trusted_schema désactivé, schéma de tables ordinaires contrôlé, SQL contextualisé
et parcours ligne à ligne. Ne jamais migrer, réparer ni checkpoint le fichier
reçu. Le store de destination conserve ses validations et son propriétaire
explicitement autorisé.

## Raisons et alternatives

Réutiliser `store.Open` déclenche des travaux de migration/réparation avant la
lecture et sans le contexte de la requête. Un timeout HTTP ne suffit donc pas.
Un lecteur dédié garde la compatibilité des tables historiques sans exécuter de
vues, triggers, colonnes générées ni tables virtuelles. La réparation compatible
des paires de buts historiques reste une conversion pure en mémoire lorsque
le marqueur correspondant manque ; aucune conversion de vitesse par seuil
numérique n’est ajoutée au lecteur. Il ne remplace pas un
sandbox SQLite/processus ni un quota mémoire/disque strict.

## Mise en œuvre et vérification

`internal/server/import_source.go`, tests de schéma hostile, compatibilité
historique, interruption SQL et libération de capacité. Les limites et la reprise
partielle sont décrites dans [OPERATIONS](../OPERATIONS.md). L'acceptation de la
proposition appartient aux mainteneurs.
