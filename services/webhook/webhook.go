package webhook

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/config"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage/postgresql"
	"github.com/hngprojects/telex_be/services/channel"
	"github.com/hngprojects/telex_be/utility"
)

func CreateWebhook(req models.CreateWebhookRequest, db *gorm.DB) (models.Webhook, int, error) {

	webhook := models.Webhook{
		ID:        utility.GenerateUUID(),
		ChannelId: req.ChannelID,
		OwnerId:   req.UserID,
		Status:    "active",
	}

	slug := req.ChannelID
	webhookUrl := config.Config.App.WebhookApiUrl + fmt.Sprintf("/v1/webhooks/%s", slug)
	webhook.WebhookSlug = slug
	webhook.WebhookUrl = webhookUrl

	err := webhook.CreateWebhook(db)

	if err != nil {
		return webhook, http.StatusBadRequest, err
	}

	return webhook, http.StatusCreated, nil
}

func DeleteWebhook(req models.DeleteWebhookRequest, db *gorm.DB) (int, error) {

	webhook := models.Webhook{
		ChannelId: req.ChannelID,
		ID:        req.WebhookID,
		OwnerId:   req.UserID,
	}

	_, err := webhook.GetWebhookByID(db, webhook.ID, webhook.ChannelId)

	if err != nil {
		return http.StatusBadRequest, err
	}

	err = webhook.DeleteWebhook(db)

	if err != nil {
		return http.StatusBadRequest, err
	}

	return http.StatusOK, nil
}

func UpdateWebhook(req models.UpdateWebhookRequest, db *gorm.DB) (models.Webhook, int, error) {

	var (
		resp models.Webhook
	)

	resp, err := resp.UpdateWebhook(db, req)

	if err != nil {
		return resp, http.StatusBadRequest, err
	}

	return resp, http.StatusOK, nil
}

func ChangeWebhookStatus(req models.ChangeWebhookStatusRequest, db *gorm.DB) (models.Webhook, int, error) {

	var (
		resp models.Webhook
		ch   models.Channels
	)

	exists := postgresql.CheckExists(db, &ch, "id = ?", req.ChannelID)
	if !exists {
		return resp, http.StatusNotFound, errors.New("channel does not exist")
	}

	if !channel.CanManageChannelRestrictions(db, ch, req.UserID) {
		return resp, http.StatusForbidden, errors.New("permission denied")
	}

	resp, err := resp.UpdateWebhookStatus(db, req)

	if err != nil {
		return resp, http.StatusBadRequest, err
	}

	return resp, http.StatusOK, nil

}

func GetAllWebhook(db *gorm.DB, c *gin.Context, req models.ChannelInfo) ([]models.Webhook, postgresql.PaginationResponse, int, error) {

	var (
		resp     []models.Webhook
		webhooks models.Webhook
	)

	if code, err := checkChannelMember(db, req); err != nil {
		return resp, postgresql.PaginationResponse{}, code, err
	}

	resp, pagResp, err := webhooks.GetAllChannelWebhook(db, c, req.ChannelID)

	if err != nil {
		return resp, pagResp, http.StatusBadRequest, err
	}

	return resp, pagResp, http.StatusOK, nil

}

func GetWebhookHistory(req models.GetWebhookHistoryRequest, c *gin.Context, db *gorm.DB) ([]models.HistoryWebhook, postgresql.PaginationResponse, int, error) {

	var (
		HistoryWebhook models.HistoryWebhook
	)

	resp, pagResp, code, err := HistoryWebhook.GetWebHookHistory(db, c, req)

	if err != nil {
		return resp, pagResp, code, err
	}

	return resp, pagResp, http.StatusOK, nil

}

func GetChannelWebhook(db *gorm.DB, c *gin.Context, channelId, userId string) (models.Webhook, int, error) {

	var (
		resp     models.Webhook
		webhooks models.Webhook
		chanReq  models.ChannelInfo
	)
	chanReq.ChannelID = channelId
	chanReq.UserID = userId

	if code, err := checkChannelMember(db, chanReq); err != nil {
		return resp, code, err
	}

	resp, err := webhooks.GetChannelWebhook(db, chanReq)

	if err != nil {
		return resp, http.StatusBadRequest, err
	}

	return resp, http.StatusOK, nil

}

// checkChannelMember reports 403 unless the user belongs to the channel. An
// unknown channel is answered the same way so that ids cannot be probed.
func checkChannelMember(db *gorm.DB, req models.ChannelInfo) (int, error) {
	if !postgresql.CheckExistsInTable(db, "user_channels", "channels_id = ? AND user_id = ?", req.ChannelID, req.UserID) {
		return http.StatusForbidden, errors.New("you are not a member of this channel")
	}

	return http.StatusOK, nil
}
