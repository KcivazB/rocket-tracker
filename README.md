# Rocket Tracker

Suit automatiquement toutes tes parties Rocket League via l'**API Stats officielle** du jeu
et affiche ta progression dans un dashboard local : http://localhost:8765

## Installation (une seule fois)

1. Ferme Rocket League.
2. Lance `dist\rltracker.exe setup` :
   - active l'API Stats dans `<RL>\TAGame\Config\DefaultStatsAPI.ini` (`PacketSendRate=30`, demande les droits admin) ;
   - ajoute le tracker au démarrage de Windows ;
   - démarre le tracker et ouvre le dashboard.
3. Lance Rocket League et joue : chaque match est enregistré automatiquement.

> Une mise à jour du jeu peut réinitialiser le fichier ini : le dashboard affiche alors un avertissement,
> il suffit de relancer `rltracker setup`.

## Le dashboard

- **Tableau de bord** : winrate, progression, analyse mentale, mécanique, arènes, coéquipiers…
- **Objectif saison** : calendrier des games par jour (objectif réglable, 10 games de 1v1 par défaut),
  séries, projection. Clique sur un jour pour ajouter des games jouées sans le tracker.
- **Historique** : tous les matchs jour par jour, avec filtres et recherche par joueur ou arène.
- **Page match** : score, tableau des scores, chronologie des buts, comparaison à ta moyenne,
  mouvement et frappes, liens tracker.gg vers les profils des joueurs.

## Commandes

| Commande | Rôle |
|---|---|
| `rltracker` / `rltracker run` | tracker + dashboard (ce que lance Windows au démarrage) |
| `rltracker setup` | active l'API Stats + démarrage auto |
| `rltracker open` | ouvre le dashboard |
| `rltracker uninstall` | retire le démarrage auto |
| `rltracker simulate` | faux serveur de jeu pour tester sans Rocket League |

Données : `%APPDATA%\RocketTracker\` (`rltracker.db` SQLite, `config.json`, `rltracker.log`).
Export CSV (Excel FR, séparateur `;`) : bouton « Export CSV » ou http://localhost:8765/api/export.csv.

## Limites

- L'API ne fournit ni le **MMR** ni la **playlist** (classé/occasionnel) : le mode (1v1/2v2/3v3) est déduit du
  nombre de joueurs, et le type de match est un tag modifiable dans le dashboard (défaut réglable).
- Tu es identifié automatiquement (caméra du joueur) ; en cas de souci, renseigne ton pseudo dans Réglages.

## Dev

```powershell
.\build.ps1 -Test        # vet + tests + build dist\rltracker.exe
.\build.ps1 -Dev         # build console (logs visibles)
```
Détails techniques : `docs/SPEC.md`, `docs/BACKEND_NOTES.md`.
