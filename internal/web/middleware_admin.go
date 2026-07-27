package web

import (
	"net/http"
	"strings"
)

// requireAdmin lets only admins through. It is the access control for /admin
// and every /api/admin/* route (Tasks 8 to 10), never the visibility of a nav
// link. A signed-in non-admin is refused with 403: they are authenticated,
// so 401 would be wrong, and a plain reject (rather than redirecting them
// somewhere) is the honest answer to "you are who you say you are, and that
// is not enough."
//
// db.PurgeItem and media.Store.Purge take no actor parameter of their own;
// authorizing a permanent delete is entirely the caller's job. Task 10 wires
// its purge endpoint through this gate, so this is the only thing standing
// between a signed-in non-admin and permanently destroying someone else's
// media. Anonymous requests are handled first by Server.Gate, which redirects
// them to /login before requireAdmin ever runs; the anonymous branch below is
// defence in depth only, in case requireAdmin is ever mounted on a route the
// Gate does not cover.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := CurrentUser(r)
		if !ok {
			if strings.HasPrefix(routedPath(r), "/api/") {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			} else {
				http.Redirect(w, r, "/login", http.StatusFound)
			}
			return
		}
		if !user.IsAdmin {
			if strings.HasPrefix(routedPath(r), "/api/") {
				writeJSONError(w, http.StatusForbidden, "forbidden", "admin only")
			} else {
				http.Error(w, "Forbidden. This page is for admins.", http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
