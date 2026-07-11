package lib

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	defaultPollInterval         = 5 * time.Second
	appStateBookingEnabledKey   = "booking_enabled"
	appStateBookingTimeSlotsKey = "booking_time_slots"
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
	initialPoll := !a.bookingPollInitialized
	becameEnabled := current && (initialPoll || !hasPrev || !prev)
	becameDisabled := !current && (initialPoll || (hasPrev && prev))
	if becameEnabled {
		if hasPrev && !prev {
			log.Printf("[poll.booking] transition off->on")
		}
		timeSlots, ok, err := a.fetchTimeSlots()
		if err != nil {
			log.Printf("[poll.booking] fetch time_slots failed err=%v", err)
			return err
		}
		if !ok {
			timeSlots = nil
		}
		if err := a.setBookingTimeSlots(timeSlots); err != nil {
			log.Printf("[poll.booking] store time_slots failed err=%v", err)
			return err
		}
	}
	if becameDisabled {
		if err := a.setBookingTimeSlots(nil); err != nil {
			log.Printf("[poll.booking] clear time_slots failed err=%v", err)
			return err
		}
	}

	if current {
		timeSlots, ok, err := a.getBookingTimeSlots()
		if err != nil {
			log.Printf("[poll.booking] load cached time_slots failed err=%v", err)
			return err
		}
		if !ok || !hasReasonableTimeSlot(timeSlots) {
			log.Printf("[poll.booking] skip booking handling because cached time_slots are unavailable or invalid")
		} else if !isBookingHintAllowedUTC(time.Now().UTC()) {
			log.Printf("[poll.booking] skip booking handling because time window closed")
		} else {
			log.Printf("[poll.booking] booking open detected cached_time_slots=%v", timeSlots)
			if hasPrev && !prev {
				if err := a.sendBookingOpenNotification(); err != nil {
					log.Printf("[poll.booking] send booking open notification failed err=%v", err)
					return err
				}
			}
			if err := a.runAutoReservations(timeSlots); err != nil {
				log.Printf("auto reservation failed: %v", err)
			}
		}
	}

	if err := a.setAppBool(appStateBookingEnabledKey, current); err != nil {
		log.Printf("[poll.booking] store state failed booking_enabled=%t err=%v", current, err)
		return err
	}
	a.bookingPollInitialized = true
	log.Printf("[poll.booking] tick done booking_enabled=%t dur=%s", current, time.Since(start))
	return nil
}

func (a *App) setBookingTimeSlots(timeSlots []string) error {
	encoded, err := json.Marshal(timeSlots)
	if err != nil {
		return err
	}
	return a.setAppString(appStateBookingTimeSlotsKey, string(encoded))
}

func (a *App) getBookingTimeSlots() ([]string, bool, error) {
	encoded, ok, err := a.getAppString(appStateBookingTimeSlotsKey)
	if err != nil || !ok {
		return nil, ok, err
	}
	var timeSlots []string
	if err := json.Unmarshal([]byte(encoded), &timeSlots); err != nil {
		return nil, false, err
	}
	return timeSlots, true, nil
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
