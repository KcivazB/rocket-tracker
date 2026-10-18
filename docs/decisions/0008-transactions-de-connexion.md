# ADR-0008 — Lier la connexion au navigateur et à l’origine

- Statut : **Proposé**
- Contexte : SEC5 de l’issue #9, fixation de cookie depuis un domaine frère.

## Décision proposée

Sous HTTPS, utiliser des cookies `__Host-` Secure, Path=/ sans Domain. Conserver
state, nonce et vérificateur PKCE côté serveur, avec un identifiant opaque lié
au navigateur et à l'origine configurée. Consommer la transaction une seule fois,
y compris en échec, avec expiration et capacité bornées. Le chemin de retour est borné à 2048 octets
(repli `/`) et copié pour ne pas retenir le backing de la query complète. Seul HTTP loopback
explicitement configuré conserve les cookies de développement non préfixés.

## Alternatives et conséquences

Signer un état dans un cookie ne suffit pas à empêcher sa fixation par un domaine
frère. Les transactions en mémoire nécessitent que login et callback atteignent
la même instance et sont perdues au redémarrage ; les sessions établies restent
persistées. Un store partagé nécessiterait un contrat de réplication distinct.
La migration de namespace demande une nouvelle connexion. L'application dépend
de l'application des préfixes par le navigateur et d'un HTTPS canonique.

## Mise en œuvre et vérification

`oidc.go` et `hub.go`, tests de rejeu/concurrence/expiration/capacité, faux IdP
cryptographique et contrat des cookies. Voir [SELF_HOSTING](../SELF_HOSTING.md)
pour la reprise ; les tests cookiejar ne qualifient pas le navigateur réel.
L'acceptation de la proposition appartient aux mainteneurs.
