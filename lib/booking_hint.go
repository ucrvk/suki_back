package lib

import (
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	defaultPollInterval       = 5 * time.Second
	appStateBookingEnabledKey = "booking_enabled"
)

var timeSlotPattern = regexp.MustCompile(`^21.*-22.*$`)

func (a *App) startBookingPoller() {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		log.Printf("[poll.booking] tick start")
		if err := a.pollBookingEnabled(); err != nil {
			log.Printf("booking poll failed: %v", err)
		} else {
			log.Printf("[poll.booking] tick ok")
		}
	}
}

func (a *App) pollBookingEnabled() error {
	start := time.Now()
	current, ok, err := a.fetchBookingEnabled()
	if err != nil {
		log.Printf("[poll.booking] fetch booking_enabled failed err=%v", err)
		return err
	}
	if !ok {
		log.Printf("[poll.booking] booking_enabled empty dur=%s", time.Since(start))
		return nil
	}

	prev, hasPrev, err := a.getAppBool(appStateBookingEnabledKey)
	if err != nil {
		log.Printf("[poll.booking] load app state failed err=%v", err)
		return err
	}
	if !hasPrev {
		log.Printf("[poll.booking] initialize state booking_enabled=%t", current)
		return a.setAppBool(appStateBookingEnabledKey, current)
	}

	if !prev && current {
		log.Printf("[poll.booking] transition off->on")
		if !isBookingHintAllowedUTC(time.Now().UTC()) {
			log.Printf("[poll.booking] skip notify because time window closed")
			return a.setAppBool(appStateBookingEnabledKey, current)
		}
		timeSlots, ok, err := a.fetchTimeSlots()
		if err != nil {
			log.Printf("[poll.booking] fetch time_slots failed err=%v", err)
			return err
		}
		if ok && hasReasonableTimeSlot(timeSlots) {
			log.Printf("[poll.booking] booking open detected time_slots=%v", timeSlots)
			if err := a.sendBookingOpenNotification(); err != nil {
				log.Printf("[poll.booking] send booking open notification failed err=%v", err)
				return err
			}
			if err := a.runAutoReservations(); err != nil {
				log.Printf("auto reservation failed: %v", err)
			}
		}
	}

	if err := a.setAppBool(appStateBookingEnabledKey, current); err != nil {
		log.Printf("[poll.booking] store state failed booking_enabled=%t err=%v", current, err)
		return err
	}
	log.Printf("[poll.booking] tick done booking_enabled=%t dur=%s", current, time.Since(start))
	return nil
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
