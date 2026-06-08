package main

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"firebase.google.com/go/v4/messaging"
)

const (
	defaultPollInterval       = 10 * time.Second
	appStateBookingEnabledKey = "booking_enabled"
	fcmTopicBookingOpen       = "booking_open"
)

var timeSlotPattern = regexp.MustCompile(`^21.*-22.*$`)

func (a *App) startBookingPoller() {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		if err := a.pollBookingEnabled(); err != nil {
			log.Printf("booking poll failed: %v", err)
		}
	}
}

func (a *App) pollBookingEnabled() error {
	current, ok, err := a.fetchBookingEnabled()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	prev, hasPrev, err := a.getAppBool(appStateBookingEnabledKey)
	if err != nil {
		return err
	}
	if !hasPrev {
		return a.setAppBool(appStateBookingEnabledKey, current)
	}

	if !prev && current {
		if !isBookingHintAllowedUTC(time.Now().UTC()) {
			return a.setAppBool(appStateBookingEnabledKey, current)
		}
		timeSlots, ok, err := a.fetchTimeSlots()
		if err != nil {
			return err
		}
		if ok && hasReasonableTimeSlot(timeSlots) {
			if err := a.sendBookingOpenNotification(); err != nil {
				return err
			}
			if err := a.runAutoReservations(); err != nil {
				log.Printf("auto reservation failed: %v", err)
			}
		}
	}

	return a.setAppBool(appStateBookingEnabledKey, current)
}

func isBookingHintAllowedUTC(now time.Time) bool {
	switch now.Weekday() {
	case time.Friday, time.Saturday:
		return true
	default:
		return false
	}
}

func hasReasonableTimeSlot(timeSlots []string) bool {
	for _, slot := range timeSlots {
		if timeSlotPattern.MatchString(strings.TrimSpace(slot)) {
			return true
		}
	}
	return false
}

func (a *App) sendBookingOpenNotification() error {
	_, err := a.fcmClient.Send(context.Background(), &messaging.Message{
		Topic: fcmTopicBookingOpen,
		Notification: &messaging.Notification{
			Title: "预约开放了",
			Body:  "赶快来预约吧！再不预约都没得预约了！",
		},
		Data: map[string]string{
			"type": "booking_open",
			"time": time.Now().UTC().Format(time.RFC3339),
		},
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				ChannelID: "default",
				Sound:     "default",
			},
		},
	})
	return err
}
