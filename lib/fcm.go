package lib

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"firebase.google.com/go/v4/messaging"
)

const fcmTopicBookingOpen = "booking_open"

var defaultFCMAndroidConfig = &messaging.AndroidConfig{
	Priority: "high",
	Notification: &messaging.AndroidNotification{
		ChannelID: "default",
		Sound:     "default",
	},
}

func (a *App) sendFCMMessage(ctx context.Context, message *messaging.Message) error {
	if a.fcmClient == nil {
		return errors.New("fcm client not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := a.fcmClient.Send(ctx, message)
	return err
}

func normalizeFCMData(data map[string]string) map[string]string {
	if len(data) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(data))
	for key, value := range data {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		cleaned[key] = strings.TrimSpace(value)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func (a *App) sendFCMTopicNotification(ctx context.Context, topic, title, body string, data map[string]string) error {
	message := &messaging.Message{
		Topic:        strings.TrimSpace(topic),
		Notification: &messaging.Notification{Title: title, Body: body},
		Data:         normalizeFCMData(data),
		Android:      defaultFCMAndroidConfig,
	}
	return a.sendFCMMessage(ctx, message)
}

func (a *App) sendFCMTokenNotification(ctx context.Context, token, title, body string, data map[string]string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	message := &messaging.Message{
		Token:        token,
		Notification: &messaging.Notification{Title: title, Body: body},
		Data:         normalizeFCMData(data),
		Android:      defaultFCMAndroidConfig,
	}
	return a.sendFCMMessage(ctx, message)
}

func (a *App) subscribeFCMTopic(ctx context.Context, token, topic string) (*messaging.TopicManagementResponse, error) {
	if a.fcmClient == nil {
		return nil, errors.New("fcm client not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return a.fcmClient.SubscribeToTopic(ctx, []string{strings.TrimSpace(token)}, strings.TrimSpace(topic))
}

func (a *App) unsubscribeFCMTopic(ctx context.Context, token, topic string) (*messaging.TopicManagementResponse, error) {
	if a.fcmClient == nil {
		return nil, errors.New("fcm client not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return a.fcmClient.UnsubscribeFromTopic(ctx, []string{strings.TrimSpace(token)}, strings.TrimSpace(topic))
}

func (a *App) sendBookingOpenNotification() error {
	return a.sendFCMTopicNotification(context.Background(), fcmTopicBookingOpen, "预约开放了", "快来预约吧，再不预约都没得预约了！", map[string]string{
		"type": "booking_open",
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *App) sendSysbookingDirectMessage(sessionRecord sysbookingSessionRecord, title, body string, data map[string]string) error {
	return a.sendFCMTokenNotification(context.Background(), sessionRecord.FCMToken.String, title, body, data)
}

func (a *App) notifyBookingSuccess(sessionRecord sysbookingSessionRecord, maid Maid, timeSlotLabel string) error {
	return a.sendSysbookingDirectMessage(sessionRecord, "预约成功", fmt.Sprintf("%s %s 已成功预约", strings.TrimSpace(maid.Name), timeSlotLabel), map[string]string{
		"type":       "booking_success",
		"maid_id":    strings.TrimSpace(maid.ID),
		"maid_vrcid": strings.TrimSpace(maid.VRCID),
		"time_slot":  timeSlotLabel,
	})
}

func (a *App) notifyBookingTokenInvalid(sessionRecord sysbookingSessionRecord, maid Maid, timeSlotLabel string) error {
	return a.sendSysbookingDirectMessage(sessionRecord, "预约 token 失效", fmt.Sprintf("%s %s 的预约 token 失效，请重新登录补充 token", strings.TrimSpace(maid.Name), timeSlotLabel), map[string]string{
		"type":       "booking_token_invalid",
		"maid_id":    strings.TrimSpace(maid.ID),
		"maid_vrcid": strings.TrimSpace(maid.VRCID),
		"time_slot":  timeSlotLabel,
	})
}

func (a *App) notifySysbookingSessionRefreshInvalid(session sysbookingSessionRecord, reason string) error {
	body := "请重新登录补充 token"
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		body = fmt.Sprintf("%s: %s", body, trimmed)
	}
	return a.sendSysbookingDirectMessage(session, "登录 token 失效", body, map[string]string{
		"type":   "session_refresh_invalid",
		"reason": strings.TrimSpace(reason),
	})
}
