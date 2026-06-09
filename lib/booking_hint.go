package lib

import (
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	defaultPollInterval       = 10 * time.Second
	appStateBookingEnabledKey = "booking_enabled"
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
