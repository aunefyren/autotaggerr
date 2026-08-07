package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/mail"
	"github.com/aunefyren/autotaggerr/settings"
	"github.com/gin-gonic/gin"
)

// getSettings returns the whole settings surface — sections, fields, current values —
// so the page renders from one description instead of hard-coding the same field list
// a second time in TypeScript. Secrets carry only whether they are set.
func (a *API) getSettings(c *gin.Context) {
	c.JSON(http.StatusOK, settings.Current())
}

// updateSettings validates and saves a partial set of edits.
//
// Partial by design: the page sends only what the user touched, so two admins editing
// different sections do not overwrite each other, and a secret left alone is expressed
// by not sending it at all (rather than by sending back the mask, which is how a
// masked field ends up saved as "••••••").
func (a *API) updateSettings(c *gin.Context) {
	var body struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if len(body.Values) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no settings were sent"})
		return
	}

	result, err := settings.Save(a.Settings, body.Values)
	if err != nil {
		// A rejected value is the user's to fix, and the message names the field and
		// what it expected, so it is returned verbatim.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// revealSecret returns one secret's stored value. It is logged with the requesting
// user, because "who looked at the SMTP password" is a question worth being able to
// answer, and a page that shows a secret should leave a trace that it did.
func (a *API) revealSecret(c *gin.Context) {
	key := c.Param("key")
	value, err := settings.Reveal(key)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if user, ok := auth.CurrentUser(c); ok {
		logger.Log.Infof("%s revealed the stored value of %s", user.Username, key)
	}
	c.JSON(http.StatusOK, gin.H{"key": key, "value": value})
}

// sendTestEmail sends one message through the configured SMTP server so an admin can
// find out whether the settings work.
//
// It reads the *stored* config, not the page's pending edits: a send is not a
// validation of what is typed, it is a check that what is saved can reach a server.
// The page says so next to the button rather than the endpoint guessing.
//
// A failure is a 400 carrying the SMTP error verbatim — "the SMTP server rejected the
// credentials: 535 authentication failed" is the whole point of the feature, and
// flattening it to "could not send" would throw away the only useful part.
func (a *API) sendTestEmail(c *gin.Context) {
	var body struct {
		To string `json:"to"`
	}
	// The body is optional: with no address the configured test recipient is used.
	_ = c.ShouldBindJSON(&body)

	recipient, err := mail.SendTest(files.ConfigFile, body.To)
	if err != nil {
		logger.Log.Warnf("test email failed: %s", err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if user, ok := auth.CurrentUser(c); ok {
		logger.Log.Infof("%s sent a test email to %s", user.Username, recipient)
	}
	c.JSON(http.StatusOK, gin.H{"sent_to": recipient})
}
