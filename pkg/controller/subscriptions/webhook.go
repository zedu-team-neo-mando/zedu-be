package subscriptions

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hngprojects/telex_be/internal/config"
	"github.com/hngprojects/telex_be/internal/models"
	service "github.com/hngprojects/telex_be/services/subscription"
	"github.com/hngprojects/telex_be/utility"
	"github.com/stripe/stripe-go/v72"
	"github.com/stripe/stripe-go/v72/checkout/session"
	"github.com/stripe/stripe-go/v72/webhook"
)

// webhookSecret returns the Stripe signing secret, or "" when none is configured.
func (base *Controller) webhookSecret() string {
	if base.StripeWebhookSecret != "" {
		return base.StripeWebhookSecret
	}
	if cfg := config.GetConfig(); cfg != nil {
		return cfg.Stripe.STRIPE_WEBHOOK_SECRET
	}
	return ""
}

// fetchCheckoutSession asks Stripe for its own copy of a checkout session.
func (base *Controller) fetchCheckoutSession(id string) (*stripe.CheckoutSession, error) {
	if base.GetCheckoutSession != nil {
		return base.GetCheckoutSession(id)
	}
	return session.Get(id, nil)
}

func (base *Controller) HandleStripeWebhook(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		base.Logger.Error(err.Error())
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Unable to read request body", nil, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	// Only accept requests that Stripe really signed. Fail closed if no secret is set.
	secret := base.webhookSecret()
	if secret == "" {
		base.Logger.Error("STRIPE_WEBHOOK_SECRET is not set; rejecting Stripe webhook")
		rd := utility.BuildErrorResponse(http.StatusInternalServerError, "error", "Webhook is not configured", nil, nil)
		c.JSON(http.StatusInternalServerError, rd)
		return
	}

	event, err := webhook.ConstructEvent(body, c.GetHeader("Stripe-Signature"), secret)
	if err != nil {
		base.Logger.Error("Stripe webhook signature verification failed: " + err.Error())
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Invalid webhook signature", nil, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		var checkoutSession stripe.CheckoutSession

		rawObject := event.Data.Raw
		if len(rawObject) == 0 && event.Data.Object != nil {
			rawObject, _ = json.Marshal(event.Data.Object)
		}

		err := json.Unmarshal(rawObject, &checkoutSession)
		if err != nil {
			base.Logger.Error("Error parsing webhook JSON:" + err.Error())
			rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Error parsing webhook JSON", nil, nil)
			c.JSON(http.StatusBadRequest, rd)
			return
		}
		base.Logger.Info("DEBUG: Webhook session ID: %s, SuccessURL: %s\n", checkoutSession.ID, checkoutSession.SuccessURL)

		if !strings.HasPrefix(checkoutSession.SuccessURL, "https://zedu.chat") &&
			!strings.HasPrefix(checkoutSession.SuccessURL, "https://staging.zedu.chat") &&
			!strings.HasPrefix(checkoutSession.SuccessURL, "https://staging.zedu.im") {
			base.Logger.Info("Webhook not for telex")
			rd := utility.BuildSuccessResponse(http.StatusOK, "Webhook not for telex", nil)
			c.JSON(http.StatusOK, rd)
			return
		}

		var webhookData = models.ProcessedStripeWebhook{}
		isProcessed, err := webhookData.IsWebhookProcessed(base.Db.Postgresql, checkoutSession.ID)
		if err != nil {
			base.Logger.Error(fmt.Sprintf("Error checking webhook processing status: %v", err.Error()))
			rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Error checking webhook processing status", nil, nil)
			c.JSON(http.StatusBadRequest, rd)
			return
		}
		if isProcessed {
			base.Logger.Info(fmt.Sprintf("Session ID %s has already been processed", checkoutSession.ID))
			rd := utility.BuildSuccessResponse(http.StatusOK, "Webhook already processed", nil)
			c.JSON(http.StatusOK, rd)
			return
		}

		switch checkoutSession.Metadata["flow"] {
		case "credit_topup":
			// Confirm with Stripe that the session was paid, and take the ids from Stripe's copy
			stripeSession, err := base.fetchCheckoutSession(checkoutSession.ID)
			if err != nil {
				base.Logger.Error(fmt.Sprintf("Could not fetch Stripe session %s: %v", checkoutSession.ID, err.Error()))
				rd := utility.BuildErrorResponse(http.StatusBadGateway, "error", "Unable to confirm payment with Stripe", nil, nil)
				c.JSON(http.StatusBadGateway, rd)
				return
			}
			if stripeSession.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
				base.Logger.Info(fmt.Sprintf("Credit top-up session %s is not paid (status: %s)", checkoutSession.ID, stripeSession.PaymentStatus))
				rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "Payment not completed", nil, nil)
				c.JSON(http.StatusBadRequest, rd)
				return
			}

			org_id := stripeSession.Metadata["org_id"]
			plan_id := stripeSession.Metadata["plan_id"]

			_, code, err := models.TopUpOrgCredit(base.Db.Postgresql, org_id, plan_id, &checkoutSession.ID)
			if err != nil {
				rd := utility.BuildErrorResponse(code, "error", "Something went wrong", err.Error(), nil)
				c.JSON(code, rd)
				base.Logger.Error(fmt.Sprintf("Credit top-up failed for session %s: %v", checkoutSession.ID, err.Error()))
				return
			}

		case "subscription":

			req := models.CompleteSubscriptionRequest{
				StripeSessionID: checkoutSession.ID,
			}

			_, code, _, err := service.CompleteSubscriptionWebhook(req, base.Db.Postgresql)
			if err != nil {
				rd := utility.BuildErrorResponse(code, "error", "Something went wrong", err.Error(), nil)
				c.JSON(code, rd)
				base.Logger.Error(fmt.Sprintf("Subscription completion failed for session %s: %v", checkoutSession.ID, err.Error()))
				return
			}
		default:
			base.Logger.Info("Unhandled flow: %s", checkoutSession.Metadata["flow"])
		}

		newWebhookData := models.ProcessedStripeWebhook{
			ID:          utility.GenerateUUID(),
			SessionID:   checkoutSession.ID,
			ProcessedAt: time.Now(),
		}

		err = newWebhookData.MarkWebhookAsProcessed(base.Db.Postgresql)
		if err != nil {
			base.Logger.Error(fmt.Sprintf("Error marking webhook as processed: %v", err.Error()))
			rd := utility.BuildSuccessResponse(http.StatusOK, "success", nil)
			c.JSON(http.StatusOK, rd)
			return
		}

	default:
		base.Logger.Info(fmt.Sprintf("Unhandled event type: %v", event.Type))
		rd := utility.BuildSuccessResponse(http.StatusOK, "Event not handled", nil)
		c.JSON(http.StatusOK, rd)
		return
	}

	base.Logger.Info("subscription created")
	rd := utility.BuildSuccessResponse(http.StatusOK, "Subscription created successfully", nil)
	c.JSON(http.StatusOK, rd)
}
