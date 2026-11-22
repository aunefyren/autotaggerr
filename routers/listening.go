package routers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Listening accounts: where each user's plays are read from (docs/listening.md).
// Every route is scoped to the signed-in user. Nobody reads or changes another
// user's accounts, admins included: history is personal.

// listeningAccountView is an account as the Account page lists it.
type listeningAccountView struct {
	models.ListeningAccount
	TokenSet   bool   `json:"token_set"`
	ServerName string `json:"server_name,omitempty"`
	Plays      int64  `json:"plays"`
	Matched    int64  `json:"matched"`
}

func (a *API) listeningAccountViews(user uuid.UUID) ([]listeningAccountView, error) {
	var rows []models.ListeningAccount
	if err := a.DB.Where("user_id = ?", user).Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	type count struct {
		AccountID uuid.UUID
		Plays     int64
		Matched   int64
	}
	var counts []count
	if err := a.DB.Model(&models.Listen{}).Where("user_id = ?", user).
		Select("account_id, COUNT(*) AS plays, SUM(CASE WHEN recording_mb_id <> '' THEN 1 ELSE 0 END) AS matched").
		Group("account_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	byAccount := map[uuid.UUID]count{}
	for _, c := range counts {
		byAccount[c.AccountID] = c
	}
	var servers []models.MediaServer
	a.DB.Find(&servers)
	serverName := map[uuid.UUID]string{}
	for _, s := range servers {
		serverName[s.ID] = s.Name
	}
	out := make([]listeningAccountView, 0, len(rows))
	for _, r := range rows {
		v := listeningAccountView{ListeningAccount: r, TokenSet: r.Token != "",
			Plays: byAccount[r.ID].Plays, Matched: byAccount[r.ID].Matched}
		if r.MediaServerID != nil {
			v.ServerName = serverName[*r.MediaServerID]
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) listListeningAccounts(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return
	}
	out, err := a.listeningAccountViews(user.ID)
	if err != nil {
		logger.Log.Errorf("failed to list listening accounts: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list listening accounts"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// loadListeningAccount reads the :id account, if it is the caller's. Someone else's
// is a 404, the same as one that does not exist.
func (a *API) loadListeningAccount(c *gin.Context) (models.ListeningAccount, bool) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return models.ListeningAccount{}, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return models.ListeningAccount{}, false
	}
	var row models.ListeningAccount
	if err := a.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "listening account not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read the listening account"})
		}
		return models.ListeningAccount{}, false
	}
	return row, true
}

type listeningAccountInput struct {
	Source          string  `json:"source"`
	Enabled         *bool   `json:"enabled"`
	Username        *string `json:"username"`
	Token           *string `json:"token"`
	MediaServerID   *string `json:"media_server_id"`
	PlexAccountID   *string `json:"plex_account_id"`
	PlexAccountName *string `json:"plex_account_name"`
}

// createListeningAccount links a ListenBrainz user, or — for an admin, who holds the
// server token anyway — any account on a Plex server. Anyone else links Plex through
// the plex.tv sign-in (claimPlexPin), which proves which account is theirs.
func (a *API) createListeningAccount(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return
	}
	var in listeningAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	row := models.ListeningAccount{UserID: user.ID, Source: in.Source, Enabled: in.Enabled == nil || *in.Enabled}
	switch in.Source {
	case models.ListeningSourceListenBrainz:
		if in.Username == nil || strings.TrimSpace(*in.Username) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "a ListenBrainz user name is required"})
			return
		}
		row.Username = strings.TrimSpace(*in.Username)
		if in.Token != nil {
			row.Token = strings.TrimSpace(*in.Token)
		}
		var dup int64
		a.DB.Model(&models.ListeningAccount{}).Where("user_id = ? AND source = ? AND LOWER(username) = ?",
			user.ID, row.Source, strings.ToLower(row.Username)).Count(&dup)
		if dup > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "this ListenBrainz user is already linked"})
			return
		}
	case models.ListeningSourcePlex:
		if !auth.IsAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "sign in with Plex to link a Plex account"})
			return
		}
		if in.MediaServerID == nil || in.PlexAccountID == nil || strings.TrimSpace(*in.PlexAccountID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "media_server_id and plex_account_id are required"})
			return
		}
		server, err := uuid.Parse(*in.MediaServerID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media_server_id"})
			return
		}
		var count int64
		a.DB.Model(&models.MediaServer{}).Where("id = ?", server).Count(&count)
		if count == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no such media server"})
			return
		}
		row.MediaServerID = &server
		row.PlexAccountID = strings.TrimSpace(*in.PlexAccountID)
		if in.PlexAccountName != nil {
			row.PlexAccountName = strings.TrimSpace(*in.PlexAccountName)
		}
		if a.plexAccountLinked(user.ID, server, row.PlexAccountID) {
			c.JSON(http.StatusConflict, gin.H{"error": "this Plex account is already linked"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "source must be plex or listenbrainz"})
		return
	}
	if err := a.DB.Create(&row).Error; err != nil {
		logger.Log.Errorf("failed to link a listening account: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to link the account"})
		return
	}
	c.JSON(http.StatusCreated, listeningAccountView{ListeningAccount: row, TokenSet: row.Token != ""})
}

func (a *API) plexAccountLinked(user, server uuid.UUID, account string) bool {
	var n int64
	a.DB.Model(&models.ListeningAccount{}).Where("user_id = ? AND source = ? AND media_server_id = ? AND plex_account_id = ?",
		user, models.ListeningSourcePlex, server, account).Count(&n)
	return n > 0
}

// updateListeningAccount switches an account on or off, or changes a ListenBrainz
// name or token. A token absent keeps the stored one; an empty one clears it. A new
// user name starts the history again.
func (a *API) updateListeningAccount(c *gin.Context) {
	row, ok := a.loadListeningAccount(c)
	if !ok {
		return
	}
	var in listeningAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	updates := map[string]any{}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	if row.Source == models.ListeningSourceListenBrainz {
		if in.Username != nil {
			name := strings.TrimSpace(*in.Username)
			if name == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "a ListenBrainz user name is required"})
				return
			}
			if !strings.EqualFold(name, row.Username) {
				updates["username"] = name
				updates["cursor"] = nil
				if err := a.deleteListens(row.ID); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forget the old user's plays"})
					return
				}
			}
		}
		if in.Token != nil {
			updates["token"] = strings.TrimSpace(*in.Token)
		}
	}
	if len(updates) > 0 {
		if err := a.DB.Model(&models.ListeningAccount{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save the account"})
			return
		}
	}
	a.DB.First(&row, "id = ?", row.ID)
	c.JSON(http.StatusOK, listeningAccountView{ListeningAccount: row, TokenSet: row.Token != ""})
}

// deleteListens forgets an account's plays and their artists.
func (a *API) deleteListens(account uuid.UUID) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("listen_id IN (?)", tx.Model(&models.Listen{}).Select("id").Where("account_id = ?", account)).
			Delete(&models.ListenArtist{}).Error; err != nil {
			return err
		}
		return tx.Where("account_id = ?", account).Delete(&models.Listen{}).Error
	})
}

// deleteListeningAccount unlinks an account and forgets every play read from it.
func (a *API) deleteListeningAccount(c *gin.Context) {
	row, ok := a.loadListeningAccount(c)
	if !ok {
		return
	}
	if err := a.deleteListens(row.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forget the account's plays"})
		return
	}
	if err := a.DB.Delete(&models.ListeningAccount{}, "id = ?", row.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unlink the account"})
		return
	}
	c.Status(http.StatusNoContent)
}

// testListeningAccount asks the source whether the account can be read. 200 either
// way; `healthy` says which.
func (a *API) testListeningAccount(c *gin.Context) {
	row, ok := a.loadListeningAccount(c)
	if !ok {
		return
	}
	var err error
	switch row.Source {
	case models.ListeningSourceListenBrainz:
		_, err = modules.NewListenBrainzClient(row.Username, row.Token).Validate()
	case models.ListeningSourcePlex:
		client, found := components.MediaServerClients(a.DB)[uuidString(row.MediaServerID)]
		if !found {
			err = errors.New("the Plex server was removed or disabled")
		} else {
			var accounts []modules.PlexAccount
			if accounts, err = client.Accounts(); err == nil {
				err = errors.New("the account is no longer on the server")
				for _, acc := range accounts {
					if acc.ID == row.PlexAccountID {
						err = nil
					}
				}
			}
		}
	}
	out := gin.H{"healthy": err == nil}
	if err != nil {
		out["error"] = err.Error()
	}
	c.JSON(http.StatusOK, out)
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// plexServerAccounts lists one server's accounts, for an admin linking one by hand.
func (a *API) plexServerAccounts(c *gin.Context) {
	row, ok := a.loadMediaServer(c)
	if !ok {
		return
	}
	accounts, err := modules.NewPlexServer(row).Accounts()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"accounts": []modules.PlexAccount{}, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

// syncListening reads the caller's accounts now, in the background. 202; the
// accounts' last_synced_at and last_error say how it went.
func (a *API) syncListening(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return
	}
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the job runner is not available"})
		return
	}
	id := user.ID
	go func() {
		if err := a.Scan.SyncListeningFor(&id); err != nil {
			logger.Log.Infof("listening sync for %s not run: %s", user.Username, err.Error())
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"queued": true})
}

// Plex sign-in. A pin is bound to the user who started it: whoever approves a pin at
// plex.tv links *their* Plex account to the pin's Autotaggerr user, so letting
// another user claim it would hand them someone else's history.
type plexPinOwner struct {
	user    uuid.UUID
	expires time.Time
}

var (
	plexPinsMu sync.Mutex
	plexPins   = map[int64]plexPinOwner{}
)

const plexPinLifetime = 15 * time.Minute

func (a *API) createPlexPin(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return
	}
	pin, err := modules.CreatePlexPin()
	if err != nil {
		logger.Log.Warnf("failed to start a Plex sign-in: %s", err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "plex.tv did not answer"})
		return
	}
	now := time.Now()
	plexPinsMu.Lock()
	for id, owner := range plexPins {
		if now.After(owner.expires) {
			delete(plexPins, id)
		}
	}
	plexPins[pin.ID] = plexPinOwner{user: user.ID, expires: now.Add(plexPinLifetime)}
	plexPinsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"id": pin.ID, "auth_url": modules.PlexAuthURL(pin.Code)})
}

// claimPlexPin finishes a sign-in: once the user has approved the pin, it links their
// account on every enabled Plex server they can reach. 202 while the pin is still
// waiting. The plex.tv token is used for the match and then dropped; plays are read
// with each server's own token.
func (a *API) claimPlexPin(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not signed in"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pin"})
		return
	}
	plexPinsMu.Lock()
	owner, found := plexPins[id]
	plexPinsMu.Unlock()
	if !found || owner.user != user.ID || time.Now().After(owner.expires) {
		c.JSON(http.StatusNotFound, gin.H{"error": "this sign-in has expired; start it again"})
		return
	}
	pin, err := modules.CheckPlexPin(id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plex.tv did not answer"})
		return
	}
	if pin.AuthToken == "" {
		c.JSON(http.StatusAccepted, gin.H{"pending": true})
		return
	}
	plexPinsMu.Lock()
	delete(plexPins, id)
	plexPinsMu.Unlock()

	plexUser, err := modules.PlexTVUser(pin.AuthToken)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plex.tv did not say who signed in"})
		return
	}
	resources, err := modules.PlexTVResources(pin.AuthToken)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plex.tv did not list the account's servers"})
		return
	}
	var servers []models.MediaServer
	a.DB.Where("enabled = ? AND type = ?", true, models.MediaServerTypePlex).Order("name").Find(&servers)
	type outcome struct {
		Server string `json:"server"`
		Linked bool   `json:"linked"`
		Error  string `json:"error,omitempty"`
	}
	results := []outcome{}
	for _, s := range servers {
		account, err := modules.NewPlexServer(s).PlexAccountFor(plexUser, resources)
		if err != nil {
			results = append(results, outcome{Server: s.Name, Error: err.Error()})
			continue
		}
		if !a.plexAccountLinked(user.ID, s.ID, account.ID) {
			sid := s.ID
			row := models.ListeningAccount{UserID: user.ID, Source: models.ListeningSourcePlex, Enabled: true,
				MediaServerID: &sid, PlexAccountID: account.ID, PlexAccountName: account.Name}
			if err := a.DB.Create(&row).Error; err != nil {
				results = append(results, outcome{Server: s.Name, Error: "failed to save the link"})
				continue
			}
		}
		results = append(results, outcome{Server: s.Name, Linked: true})
	}
	c.JSON(http.StatusOK, gin.H{"plex_user": plexUser.Username, "servers": results})
}
