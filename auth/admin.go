package auth

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
)

// RequireAdmin rejects a request whose authenticated user is not an admin. It must be
// mounted *after* Middleware, which is what puts the user in the context; a missing
// user is treated as not authorised rather than as a programming error, so a
// mis-ordered mount fails closed.
//
// Scope note: this is the first route guard to read the role column. Every other
// endpoint remains open to any authenticated user, which is deliberate — the settings
// page can change the port, the schedules and the SMTP credentials, and that is a
// different kind of power from re-tagging an album.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := CurrentUser(c)
		if !ok || user.Role != models.UserRoleAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "this needs an admin account",
			})
			return
		}
		c.Next()
	}
}

// IsAdmin reports whether the request's user is an admin, for handlers that shape a
// response rather than refuse it.
func IsAdmin(c *gin.Context) bool {
	user, ok := CurrentUser(c)
	return ok && user.Role == models.UserRoleAdmin
}
