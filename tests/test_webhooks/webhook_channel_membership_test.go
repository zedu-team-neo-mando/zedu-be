package test_webhook

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/webhook"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

// webhookMemberFixture holds the shared state for the channel membership tests:
// a router wired with only the two routes under test, a user who belongs to
// the channels created by webhookMemberNewChannel, and a logged-in user who
// belongs to none.
type webhookMemberFixture struct {
	db            *storage.Database
	router        *gin.Engine
	orgID         string
	memberID      string
	memberToken   string
	outsiderToken string
}

func webhookMemberDelete(t *testing.T, db *storage.Database, model any, query string, args ...any) {
	t.Helper()

	if err := db.Postgresql.Unscoped().Where(query, args...).Delete(model).Error; err != nil {
		t.Errorf("cleanup of %T where %s failed: %v", model, query, err)
	}
}

// webhookMemberNewUser inserts a user with a live session and returns its id
// and bearer token, without going through registration.
func webhookMemberNewUser(t *testing.T, db *storage.Database, label string) (string, string) {
	t.Helper()

	user := models.User{
		ID:       utility.GenerateUUID(),
		Name:     fmt.Sprintf("webhook-member-%s", label),
		Email:    fmt.Sprintf("webhook-member-%s-%s@qa.team", label, utility.GenerateUUID()),
		Password: "not-a-real-password-hash",
		IsActive: true,
	}
	if err := db.Postgresql.Create(&user).Error; err != nil {
		t.Fatalf("failed to create %s user: %v", label, err)
	}
	t.Cleanup(func() { webhookMemberDelete(t, db, &models.User{}, "id = ?", user.ID) })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	tokenData, err := middleware.CreateToken(user, ctx)
	if err != nil {
		t.Fatalf("failed to create %s token: %v", label, err)
	}

	session := models.AccessToken{ID: tokenData.AccessUuid, OwnerID: user.ID}
	tokens := map[string]string{
		"access_token": tokenData.AccessToken,
		"exp":          fmt.Sprint(tokenData.ExpiresAt.Unix()),
	}
	if err := session.CreateAccessToken(db.Postgresql, tokens); err != nil {
		t.Fatalf("failed to create %s session: %v", label, err)
	}
	t.Cleanup(func() { webhookMemberDelete(t, db, &models.AccessToken{}, "id = ?", session.ID) })

	return user.ID, tokenData.AccessToken
}

func webhookMemberSetup(t *testing.T) *webhookMemberFixture {
	t.Helper()

	logger := tst.Setup()
	gin.SetMode(gin.TestMode)

	db := storage.Connection()
	fx := &webhookMemberFixture{db: db}
	fx.memberID, fx.memberToken = webhookMemberNewUser(t, db, "member")
	_, fx.outsiderToken = webhookMemberNewUser(t, db, "outsider")

	org := models.Organisation{
		ID:      utility.GenerateUUID(),
		Name:    fmt.Sprintf("WebhookMemberOrg%s", utility.GenerateUUID()),
		Email:   "webhook-member@qa.team",
		OwnerID: fx.memberID,
	}
	if err := db.Postgresql.Create(&org).Error; err != nil {
		t.Fatalf("failed to create organisation: %v", err)
	}
	t.Cleanup(func() { webhookMemberDelete(t, db, &models.Organisation{}, "id = ?", org.ID) })
	fx.orgID = org.ID

	ctrl := webhook.Controller{Db: db, Validator: validator.New(), Logger: logger}
	fx.router = gin.Default()
	group := fx.router.Group("/api/v1/webhooks", middleware.Authorize(db.Postgresql))
	group.GET("/:channel_id/all", ctrl.GetAllWebhook)
	group.GET("/:channel_id", ctrl.GetChannelWebhook)

	return fx
}

// webhookMemberNewChannel creates a channel the member belongs to and,
// optionally, a webhook for it. It returns the channel id and the webhook id
// (empty when no webhook was requested).
func webhookMemberNewChannel(t *testing.T, fx *webhookMemberFixture, withWebhook bool) (string, string) {
	t.Helper()

	channel := models.Channels{
		ID:             utility.GenerateUUID(),
		Name:           fmt.Sprintf("WebhookMemberChannel%s", utility.GenerateUUID()),
		Description:    "channel for webhook membership tests",
		OrganisationID: fx.orgID,
		OwnerId:        fx.memberID,
	}
	if err := fx.db.Postgresql.Create(&channel).Error; err != nil {
		t.Fatalf("failed to create channel: %v", err)
	}
	t.Cleanup(func() { webhookMemberDelete(t, fx.db, &models.Channels{}, "id = ?", channel.ID) })
	webhookMemberTrackChannelRows(t, fx, channel.ID)

	membership := models.UserChannels{ChannelsID: channel.ID, UserID: fx.memberID, Username: "webhook-member"}
	if err := fx.db.Postgresql.Create(&membership).Error; err != nil {
		t.Fatalf("failed to add member to channel: %v", err)
	}

	if !withWebhook {
		return channel.ID, ""
	}

	hook := models.Webhook{
		ID:          utility.GenerateUUID(),
		ChannelId:   channel.ID,
		OwnerId:     fx.memberID,
		Status:      "active",
		WebhookSlug: channel.ID,
		WebhookUrl:  "https://webhooks.qa.team/v1/webhooks/" + channel.ID,
	}
	if err := fx.db.Postgresql.Create(&hook).Error; err != nil {
		t.Fatalf("failed to create webhook: %v", err)
	}

	return channel.ID, hook.ID
}

// webhookMemberTrackChannelRows registers the cleanup of everything that is
// keyed by the channel id: the membership rows and any webhook, including one
// that a faulty handler creates while the tests run.
func webhookMemberTrackChannelRows(t *testing.T, fx *webhookMemberFixture, channelID string) {
	t.Helper()

	t.Cleanup(func() {
		webhookMemberDelete(t, fx.db, &models.Webhook{}, "channel_id = ?", channelID)
		webhookMemberDelete(t, fx.db, &models.UserChannels{}, "channels_id = ?", channelID)
	})
}

func webhookMemberGet(fx *webhookMemberFixture, path, token string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rr := httptest.NewRecorder()
	fx.router.ServeHTTP(rr, req)

	return rr
}

// webhookMemberAssertNoData fails when a rejected response carries a data
// payload or mentions the webhook that the caller must not learn about.
func webhookMemberAssertNoData(t *testing.T, rr *httptest.ResponseRecorder, webhookID string) {
	t.Helper()

	raw := rr.Body.String()
	if _, leaked := tst.ParseResponse(rr)["data"]; leaked {
		t.Errorf("response must not carry data, got body %s", raw)
	}
	if strings.Contains(raw, webhookID) {
		t.Errorf("response leaks the webhook id, got body %s", raw)
	}
}

func webhookMemberCount(t *testing.T, fx *webhookMemberFixture, channelID string) int64 {
	t.Helper()

	var count int64
	if err := fx.db.Postgresql.Model(&models.Webhook{}).Where("channel_id = ?", channelID).Count(&count).Error; err != nil {
		t.Fatalf("failed to count webhooks: %v", err)
	}

	return count
}

func TestWebhookMemberChannelAccess(t *testing.T) {
	fx := webhookMemberSetup(t)

	t.Run("member lists the channel webhooks", func(t *testing.T) {
		channelID, webhookID := webhookMemberNewChannel(t, fx, true)

		rr := webhookMemberGet(fx, "/api/v1/webhooks/"+channelID+"/all", fx.memberToken)

		tst.AssertStatusCode(t, rr.Code, http.StatusOK)
		data, ok := tst.ParseResponse(rr)["data"].(map[string]any)
		if !ok {
			t.Fatalf("expected a data object, got body %s", rr.Body.String())
		}
		hooks, ok := data["webhooks"].([]any)
		if !ok || len(hooks) != 1 {
			t.Fatalf("expected exactly one webhook, got %v", data["webhooks"])
		}
		if id := hooks[0].(map[string]any)["id"]; id != webhookID {
			t.Errorf("listed webhook id: got %v expected %s", id, webhookID)
		}
	})

	t.Run("non-member cannot list the channel webhooks", func(t *testing.T) {
		channelID, webhookID := webhookMemberNewChannel(t, fx, true)

		rr := webhookMemberGet(fx, "/api/v1/webhooks/"+channelID+"/all", fx.outsiderToken)

		tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
		webhookMemberAssertNoData(t, rr, webhookID)
	})

	t.Run("member gets the channel webhook", func(t *testing.T) {
		channelID, webhookID := webhookMemberNewChannel(t, fx, true)

		rr := webhookMemberGet(fx, "/api/v1/webhooks/"+channelID, fx.memberToken)

		tst.AssertStatusCode(t, rr.Code, http.StatusOK)
		data, ok := tst.ParseResponse(rr)["data"].(map[string]any)
		if !ok {
			t.Fatalf("expected a data object, got body %s", rr.Body.String())
		}
		if data["id"] != webhookID {
			t.Errorf("webhook id: got %v expected %s", data["id"], webhookID)
		}
		if count := webhookMemberCount(t, fx, channelID); count != 1 {
			t.Errorf("webhook rows for the channel: got %d expected 1", count)
		}
	})

	t.Run("non-member cannot read the channel webhook", func(t *testing.T) {
		channelID, webhookID := webhookMemberNewChannel(t, fx, true)

		rr := webhookMemberGet(fx, "/api/v1/webhooks/"+channelID, fx.outsiderToken)

		tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
		webhookMemberAssertNoData(t, rr, webhookID)
	})

	t.Run("non-member cannot create the channel webhook", func(t *testing.T) {
		channelID, _ := webhookMemberNewChannel(t, fx, false)

		rr := webhookMemberGet(fx, "/api/v1/webhooks/"+channelID, fx.outsiderToken)

		tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
		if count := webhookMemberCount(t, fx, channelID); count != 0 {
			t.Errorf("a webhook was created for a channel the caller is not in: got %d rows", count)
		}
	})

	t.Run("non-member gets 403 for an unknown channel", func(t *testing.T) {
		for _, suffix := range []string{"", "/all"} {
			unknownID := utility.GenerateUUID()
			webhookMemberTrackChannelRows(t, fx, unknownID)

			rr := webhookMemberGet(fx, "/api/v1/webhooks/"+unknownID+suffix, fx.outsiderToken)

			tst.AssertStatusCode(t, rr.Code, http.StatusForbidden)
			if count := webhookMemberCount(t, fx, unknownID); count != 0 {
				t.Errorf("GET %q created %d webhook rows for an unknown channel", suffix, count)
			}
		}
	})
}
