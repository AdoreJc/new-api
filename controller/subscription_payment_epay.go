package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

// ErrSubscriptionOrderMoneyMismatch is returned when the amount reported by the
// Epay callback differs from the amount the subscription order was created with.
var ErrSubscriptionOrderMoneyMismatch = errors.New("subscription order money mismatch")

// minEpayMoney is the smallest amount the Epay gateway accepts.
var minEpayMoney = decimal.NewFromFloat(0.01)

// subscriptionEpayMoney converts a plan's USD price into the amount charged by
// the Epay gateway. Subscription plans carry a fixed list price, so neither the
// top-up group ratio nor the top-up preset discounts (both keyed by top-up
// amount) apply here. The result is unrounded so the caller can reject
// sub-cent amounts before rounding to the two decimals the gateway expects.
func subscriptionEpayMoney(plan *model.SubscriptionPlan) decimal.Decimal {
	return payMoneyFromUSD(decimal.NewFromFloat(plan.PriceAmount))
}

func SubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorT(c, "Subscription plan is not enabled")
		return
	}
	payMoney := subscriptionEpayMoney(plan)
	if payMoney.LessThan(minEpayMoney) {
		common.ApiErrorT(c, "Plan amount is too low")
		return
	}
	payMoney = payMoney.Round(2)
	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		common.ApiErrorT(c, "Payment method does not exist")
		return
	}

	userId := c.GetInt("id")
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorT(c, "Purchase limit for this plan has been reached")
			return
		}
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/return")
	if err != nil {
		common.ApiErrorT(c, "Callback URL configuration error")
		return
	}
	notifyUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/notify")
	if err != nil {
		common.ApiErrorT(c, "Callback URL configuration error")
		return
	}

	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("SUBUSR%dNO%s", userId, tradeNo)

	client := GetEpayClient()
	if client == nil {
		common.ApiErrorT(c, "Payment information has not been configured by administrator")
		return
	}

	// Money is the amount actually charged by the gateway (plan USD price ×
	// price per USD), the same unit TopUp.Money uses for Epay top-ups.
	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           payMoney.InexactFloat64(),
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		common.ApiErrorT(c, "Failed to create order")
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("SUB:%s", plan.Title),
		Money:          payMoney.StringFixed(2),
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		_ = model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderEpay)
		logger.LogError(c.Request.Context(), common.LogText("Epay subscription failed to start payment user_id=%d plan_id=%d trade_no=%s payment_method=%s error=%q", userId, plan.Id, tradeNo, req.PaymentMethod, err.Error()))
		common.ApiErrorT(c, "Failed to start payment")
		return
	}
	logger.LogInfo(c.Request.Context(), common.LogText("Epay subscription order created user_id=%d plan_id=%d trade_no=%s payment_method=%s price_usd=%.2f price_per_usd=%.4f money=%s", userId, plan.Id, tradeNo, req.PaymentMethod, plan.PriceAmount, operation_setting.Price, payMoney.StringFixed(2)))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

// completeEpaySubscriptionOrder finishes a subscription order after the Epay
// signature has been verified. The callback amount must equal the amount the
// order was created with, so a tampered or mis-converted payment never grants
// a subscription.
func completeEpaySubscriptionOrder(ctx context.Context, verifyInfo *epay.VerifyRes, clientIp string) error {
	order := model.GetSubscriptionOrderByTradeNo(verifyInfo.ServiceTradeNo)
	if order == nil {
		logger.LogWarn(ctx, common.LogText("Epay subscription callback order not found trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, clientIp))
		return model.ErrSubscriptionOrderNotFound
	}
	if order.PaymentProvider != model.PaymentProviderEpay {
		logger.LogWarn(ctx, common.LogText("Epay subscription order payment gateway mismatch trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, clientIp))
		return model.ErrPaymentMethodMismatch
	}
	if !model.EpayCallbackMoneyMatches(order.Money, verifyInfo.Money) {
		logger.LogWarn(ctx, common.LogText("Epay subscription callback money mismatch trade_no=%s order_money=%.2f callback_money=%q client_ip=%s", verifyInfo.ServiceTradeNo, order.Money, verifyInfo.Money, clientIp))
		return ErrSubscriptionOrderMoneyMismatch
	}

	LockOrder(verifyInfo.ServiceTradeNo)
	defer UnlockOrder(verifyInfo.ServiceTradeNo)
	if err := model.CompleteSubscriptionOrder(verifyInfo.ServiceTradeNo, common.GetJsonString(verifyInfo), model.PaymentProviderEpay, verifyInfo.Type); err != nil {
		logger.LogError(ctx, common.LogText("Epay subscription order completion failed trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, clientIp, err.Error()))
		return err
	}
	logger.LogInfo(ctx, common.LogText("Epay subscription payment succeeded trade_no=%s callback_type=%s money=%.2f client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, order.Money, clientIp))
	return nil
}

func SubscriptionEpayNotify(c *gin.Context) {
	if !isEpayWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), common.LogText("Epay webhook rejected reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}

	if len(params) == 0 {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	client := GetEpayClient()
	if client == nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if verifyInfo.TradeStatus != epay.StatusTradeSuccess {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if err := completeEpaySubscriptionOrder(c.Request.Context(), verifyInfo, c.ClientIP()); err != nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	_, _ = c.Writer.Write([]byte("success"))
}

// SubscriptionEpayReturn handles browser return after payment.
// It verifies the payload and completes the order, then redirects to console.
func SubscriptionEpayReturn(c *gin.Context) {
	if !isEpayWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), common.LogText("Epay webhook rejected reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}

	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}

	if len(params) == 0 {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}

	client := GetEpayClient()
	if client == nil {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		if err := completeEpaySubscriptionOrder(c.Request.Context(), verifyInfo, c.ClientIP()); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
			return
		}
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=success"))
		return
	}
	c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
}
