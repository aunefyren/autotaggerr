package routers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// flowCookieName holds the signed state/nonce/PKCE bundle for one login round-trip.
const flowCookieName = "autotaggerr_oidc_flow"

// loginProvider is the public description of a login button. It is served
// unauthenticated — you need it to log in — so it carries no secrets and no issuer.
type loginProvider struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Type string    `json:"type"`
}

// listLoginProviders returns the enabled external login options for the login page.
func (a *API) listLoginProviders(c *gin.Context) {
	var providers []models.AuthProvider
	if err := a.DB.Where("enabled = ?", true).Order("name").Find(&providers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list login providers"})
		return
	}
	out := make([]loginProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, loginProvider{ID: p.ID, Name: p.Name, Type: p.Type})
	}
	c.JSON(http.StatusOK, out)
}

// externalBaseURL reconstructs the URL the browser used, so the redirect URI we
// send the provider matches the one it will redirect back to. Proxy headers win
// when present, since behind a reverse proxy the request's own scheme/host are the
// internal ones and would not match what is registered at the IdP.
func externalBaseURL(c *gin.Context) string {
	scheme := "http"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return scheme + "://" + strings.TrimSpace(host)
}

// redirectURI is the callback URL for a provider: its explicit override, or one
// derived from the incoming request.
func redirectURI(c *gin.Context, p models.AuthProvider) string {
	if u := strings.TrimSpace(p.RedirectURL); u != "" {
		return u
	}
	return externalBaseURL(c) + "/api/v1/auth/oidc/" + p.ID.String() + "/callback"
}

func (a *API) enabledProvider(c *gin.Context) (models.AuthProvider, bool) {
	var p models.AuthProvider
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return p, false
	}
	if err := a.DB.Where("id = ? AND enabled = ?", id, true).First(&p).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "login provider not found"})
		return p, false
	}
	return p, true
}

// setFlowCookie stores the signed flow bundle. HttpOnly keeps it away from page
// scripts; SameSite=Lax still allows the provider's top-level redirect back to us.
func setFlowCookie(c *gin.Context, value string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     flowCookieName,
		Value:    value,
		Path:     "/api/v1/auth/oidc",
		MaxAge:   int(auth.FlowTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearFlowCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     flowCookieName,
		Value:    "",
		Path:     "/api/v1/auth/oidc",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// startOIDCLogin redirects the browser to the provider's authorization endpoint.
func (a *API) startOIDCLogin(c *gin.Context) {
	p, ok := a.enabledProvider(c)
	if !ok {
		return
	}

	authURL, flow, err := auth.StartLogin(c.Request.Context(), p, redirectURI(c, p), a.SigningKey)
	if err != nil {
		logger.Log.Errorf("failed to start OIDC login for %s: %s", p.Name, err.Error())
		a.redirectToLogin(c, "Could not reach the login provider")
		return
	}

	setFlowCookie(c, flow, strings.HasPrefix(externalBaseURL(c), "https://"))
	c.Redirect(http.StatusFound, authURL)
}

// completeOIDCLogin handles the provider's redirect back, and on success hands the
// SPA a session token identical to the one password login issues.
func (a *API) completeOIDCLogin(c *gin.Context) {
	secure := strings.HasPrefix(externalBaseURL(c), "https://")
	defer clearFlowCookie(c, secure)

	p, ok := a.enabledProvider(c)
	if !ok {
		return
	}

	// The provider reports user-facing failures (consent denied, etc) in-band.
	if e := c.Query("error"); e != "" {
		logger.Log.Warnf("OIDC provider %s returned error: %s", p.Name, e)
		a.redirectToLogin(c, "Login was cancelled or refused by the provider")
		return
	}

	flow, err := c.Cookie(flowCookieName)
	if err != nil || flow == "" {
		a.redirectToLogin(c, "Login timed out — please try again")
		return
	}

	identity, err := auth.CompleteLogin(c.Request.Context(), p, redirectURI(c, p),
		c.Query("code"), c.Query("state"), flow, a.SigningKey)
	if err != nil {
		logger.Log.Errorf("OIDC login failed for %s: %s", p.Name, err.Error())
		a.redirectToLogin(c, "Login could not be verified")
		return
	}

	user, err := auth.ResolveUser(a.DB, p, identity)
	if err != nil {
		logger.Log.Warnf("OIDC identity %q rejected for %s: %s", identity.Subject, p.Name, err.Error())
		a.redirectToLogin(c, "No account is linked to this login")
		return
	}

	token, err := auth.IssueToken(user, a.SigningKey, auth.DefaultTokenTTL)
	if err != nil {
		logger.Log.Errorf("failed to issue token after OIDC login: %s", err.Error())
		a.redirectToLogin(c, "Could not start a session")
		return
	}

	// The token goes back in the URL *fragment*: fragments are not sent to servers
	// and do not land in access logs or the Referer header. The SPA reads it, stores
	// it, and strips it from the address bar.
	c.Redirect(http.StatusFound, "/login#token="+url.QueryEscape(token))
}

// redirectToLogin sends the browser back to the SPA login page with a message to
// show. Failures are deliberately vague to the user and detailed only in the log.
func (a *API) redirectToLogin(c *gin.Context, message string) {
	c.Redirect(http.StatusFound, "/login?error="+url.QueryEscape(message))
}
