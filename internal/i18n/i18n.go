// Package i18n holds the French and English texts of the command line and of
// the server's sign-in pages (the dashboard has its own, web/i18n.js).
package i18n

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Lang is a supported language.
type Lang int

const (
	FR Lang = iota
	EN
)

// Code returns "fr" or "en".
func (l Lang) Code() string {
	if l == FR {
		return "fr"
	}
	return "en"
}

// Parse reads a language code ("fr", "en-GB"...).
func Parse(s string) (Lang, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 2 {
		s = s[:2]
	}
	switch s {
	case "fr":
		return FR, true
	case "en":
		return EN, true
	}
	return EN, false
}

// FromEnv returns $RT_LANG when it names a supported language.
func FromEnv() (Lang, bool) { return Parse(os.Getenv("RT_LANG")) }

// FromAcceptLanguage picks the preferred supported language of an HTTP
// Accept-Language header (English when none matches).
func FromAcceptLanguage(h string) Lang {
	best, bestQ := EN, -1.0
	for _, part := range strings.Split(h, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		l, ok := Parse(tag)
		if !ok {
			continue
		}
		q := 1.0
		if v, found := strings.CutPrefix(strings.TrimSpace(params), "q="); found {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q > bestQ {
			best, bestQ = l, q
		}
	}
	return best
}

// T returns the text of key in l: a fmt format when the text has verbs.
// An unknown key returns the key itself.
func T(l Lang, key string) string {
	e, ok := messages[key]
	if !ok {
		return key
	}
	return e[l]
}

// Tf formats the text of key with args.
func Tf(l Lang, key string, args ...any) string { return fmt.Sprintf(T(l, key), args...) }

// messages: key -> [French, English].
var messages = map[string][2]string{
	// rltracker setup / agent setup (console and message box)
	"setup.title":           {"Rocket Tracker — installation", "Rocket Tracker — setup"},
	"setup.notFound":        {"Rocket League introuvable. Indiquez le dossier avec --rl-dir ou %s", "Rocket League not found. Give its folder with --rl-dir or %s"},
	"setup.dirHint":         {"rl_install_dir dans %s", "rl_install_dir in %s"},
	"setup.found":           {"Rocket League : %s", "Rocket League: %s"},
	"setup.elevate":         {"Droits administrateur requis pour modifier le fichier ini, demande d'élévation…", "Administrator rights are needed to edit the ini file, asking for elevation…"},
	"setup.enableFailed":    {"Échec de l'activation de la Stats API : %v", "Could not enable the Stats API: %v"},
	"setup.enabled":         {"Stats API activée : %s (PacketSendRate=%g, Port=%d)", "Stats API enabled: %s (PacketSendRate=%g, Port=%d)"},
	"setup.restart":         {"→ Redémarrez Rocket League si le jeu est ouvert.", "→ Restart Rocket League if the game is running."},
	"setup.checkFailed":     {"Le fichier ini a été écrit mais la vérification a échoué : %s", "The ini file was written but the check failed: %s"},
	"setup.autostart":       {`Démarrage automatique enregistré (HKCU\...\Run\%s)`, `Autostart registered (HKCU\...\Run\%s)`},
	"setup.autostartFailed": {"Démarrage automatique : échec (%v)", "Autostart: failed (%v)"},
	"setup.startFailed":     {"Impossible de lancer le tracker : %v", "Could not start the tracker: %v"},
	"setup.dashboard":       {"Tableau de bord : %s", "Dashboard: %s"},
	"error.box":             {"Erreur : %s", "Error: %s"},
	"tray.open":             {"Ouvrir le tableau de bord", "Open the dashboard"},
	"tray.update":           {"Installer la mise à jour v%s", "Install the v%s update"},
	"update.failed":         {"La mise à jour automatique a échoué : %s\n\nLa page de la nouvelle version va s'ouvrir pour la télécharger à la main.", "The automatic update failed: %s\n\nThe page of the new version will open so you can download it by hand."},
	"update.restartFailed":  {"Mise à jour installée, mais Rocket Tracker n'a pas pu redémarrer : %s\nRelancez-le à la main.", "Update installed, but Rocket Tracker could not restart: %s\nStart it again by hand."},
	"tray.quit":             {"Quitter Rocket Tracker", "Quit Rocket Tracker"},
	"uninstall.done":        {"Démarrage automatique supprimé. Les données sont conservées dans %s", "Autostart removed. Your data is kept in %s"},

	"agent.title":        {"Rocket Tracker — agent", "Rocket Tracker — agent"},
	"agent.needArgs":     {"agent setup demande --server URL et --token JETON (créez le jeton dans le tableau de bord : Appareils)", "agent setup requires --server URL and --token TOKEN (create the token in the dashboard: Devices)"},
	"agent.unreachable":  {"connexion au serveur %s impossible : %w", "cannot reach the server %s: %w"},
	"agent.connected":    {"Connecté à %s en tant que %s (appareil « %s »)", "Connected to %s as %s (device \"%s\")"},
	"agent.localRunning": {"Le tracker local (rltracker run) tourne encore : quittez-le (ou redémarrez Windows) pour que seul l'agent suive les matchs.", "The local tracker (rltracker run) is still running: quit it (or restart Windows) so that only the agent tracks matches."},
	"agent.localMatches": {"%d match(s) enregistré(s) localement : envoyez-les au serveur avec « rltracker agent import ».", "%d match(es) recorded locally: send them to the server with \"rltracker agent import\"."},
	"agent.startFailed":  {"Impossible de lancer l'agent : %v", "Could not start the agent: %v"},
	"import.notFound":    {"base de données introuvable : %s", "database not found: %s"},
	"import.uploading":   {"Envoi de %s vers %s…\n", "Uploading %s to %s…\n"},
	"import.progress":    {"  %d / %d matchs\n", "  %d / %d matches\n"},
	"import.stopped":     {"import interrompu après %d match(s) (relancer la commande reprend sans doublons) : %w", "import stopped after %d match(es) (run the command again to resume, without duplicates): %w"},
	"import.done":        {"Terminé : %d match(s) et %d jour(s) de games saisies à la main envoyés.\n", "Done: %d match(es) and %d day(s) of hand-entered games sent.\n"},

	// rltracker-server sign-in pages
	"auth.devTitle":     {"Connexion (développement)", "Sign in (development)"},
	"auth.devPlayer":    {"Joueur", "Player"},
	"auth.devSubmit":    {"Entrer", "Sign in"},
	"auth.devNote":      {"Mode de test sans fournisseur d’identité : n’importe qui peut se connecter sous n’importe quel nom.", "Test mode without an identity provider: anyone can sign in under any name."},
	"auth.unavailable":  {"Connexion indisponible", "Sign-in unavailable"},
	"auth.noProvider":   {"Aucun fournisseur d’identité n’est configuré.", "No identity provider is configured."},
	"auth.idpDown":      {"Fournisseur d’identité injoignable", "Identity provider unreachable"},
	"auth.idpDownText":  {"Le serveur de connexion ne répond pas. Réessayez dans un instant.", "The sign-in server does not answer. Try again in a moment."},
	"auth.retry":        {"Réessayer", "Try again"},
	"auth.refused":      {"Connexion refusée", "Sign-in refused"},
	"auth.expired":      {"Session de connexion expirée", "Sign-in expired"},
	"auth.expiredText":  {"La connexion a pris trop de temps ou a été ouverte dans un autre onglet.", "Signing in took too long or was started in another tab."},
	"auth.failed":       {"Échec de la connexion", "Sign-in failed"},
	"auth.codeRefused":  {"Le code de connexion a été refusé.", "The authorization code was refused."},
	"auth.tokenInvalid": {"Le jeton d’identité est invalide.", "The ID token is invalid."},
	"auth.nonce":        {"Le jeton d’identité ne correspond pas à cette connexion.", "The ID token does not match this sign-in."},
	"auth.claims":       {"Le jeton d’identité est illisible.", "The ID token cannot be read."},
	"auth.denied":       {"Accès refusé", "Access denied"},
	"auth.notInGroup":   {"Votre compte n’appartient à aucun groupe autorisé pour Rocket Tracker.", "Your account is not in any group allowed to use Rocket Tracker."},
	"auth.saveAccount":  {"Impossible d’enregistrer le compte.", "Could not save the account."},
	"auth.session":      {"Impossible d’ouvrir la session.", "Could not open the session."},
	"auth.signedOut":    {"Vous êtes déconnecté", "You are signed out"},
	"auth.signInAgain":  {"Se reconnecter", "Sign in again"},
}
