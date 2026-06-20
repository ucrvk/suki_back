package lib

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

func (a *App) runAutoReservations() error {
	start := time.Now()
	log.Printf("[auto_reservation] start")
	rows, err := a.fetchBookingRows()
	if err != nil {
		log.Printf("[auto_reservation] fetch booking rows failed err=%v", err)
		return err
	}
	if len(rows) == 0 {
		log.Printf("[auto_reservation] no booking rows")
		return nil
	}

	row := rows[0]
	slotLabels := map[int]string{}
	for _, raw := range row.TimeSlots {
		slot, label, ok := normalizeReservationTimeSlot(raw)
		if !ok {
			continue
		}
		if _, exists := slotLabels[slot]; !exists {
			slotLabels[slot] = label
		}
	}
	if len(slotLabels) == 0 {
		log.Printf("[auto_reservation] no valid time slots")
		return nil
	}

	for _, maid := range row.Maids {
		if maid.Disabled {
			continue
		}
		for _, slot := range []int{21, 22} {
			label, ok := slotLabels[slot]
			if !ok {
				continue
			}
			log.Printf("[auto_reservation] process maid_id=%s vrcid=%s slot=%d label=%s", maid.ID, maid.VRCID, slot, label)
			if err := a.processReservationQueueForMaidSlot(maid, slot, label); err != nil {
				log.Printf("auto reservation queue failed: maid_id=%s vrcid=%s slot=%d err=%v", maid.ID, maid.VRCID, slot, err)
			}
		}
	}
	log.Printf("[auto_reservation] done dur=%s", time.Since(start))
	return nil
}

func (a *App) processReservationQueueForMaidSlot(maid Maid, slot int, timeSlotLabel string) error {
	for _, maidID := range uniqueStrings(strings.TrimSpace(maid.ID), strings.TrimSpace(maid.VRCID)) {
		log.Printf("[auto_reservation] lookup head maid_id=%s slot=%d", maidID, slot)
		booking, ok, err := a.getWaitingSysbookingBookingHead(maidID, slot)
		if err != nil {
			log.Printf("[auto_reservation] lookup head failed maid_id=%s slot=%d err=%v", maidID, slot, err)
			return err
		}
		if !ok {
			log.Printf("[auto_reservation] no waiting booking maid_id=%s slot=%d", maidID, slot)
			continue
		}
		log.Printf("[auto_reservation] head booking_id=%s user_id=%s maid_id=%s slot=%d", booking.BookingID, booking.UserID, maidID, slot)
		if err := a.attemptAutoReservation(maid, timeSlotLabel, booking); err != nil {
			log.Printf("auto reservation attempt failed: booking_id=%s user_id=%s err=%v", booking.BookingID, booking.UserID, err)
		}
		return nil
	}
	return nil
}

func (a *App) attemptAutoReservation(maid Maid, timeSlotLabel string, booking sysbookingBookingRecord) error {
	start := time.Now()
	log.Printf("[auto_reservation] attempt start booking_id=%s user_id=%s maid_id=%s slot=%s", booking.BookingID, booking.UserID, maid.ID, timeSlotLabel)
	sessionRecord, err := a.getSysbookingSession(booking.UserID)
	if err != nil {
		log.Printf("[auto_reservation] load session failed user_id=%s err=%v", booking.UserID, err)
		return err
	}
	if !sessionRecord.TokenValid {
		log.Printf("[auto_reservation] skip invalid token user_id=%s", booking.UserID)
		return nil
	}
	if !sessionRecord.FCMToken.Valid || strings.TrimSpace(sessionRecord.FCMToken.String) == "" {
		// No device token available for notifications. Continue the reservation flow.
		log.Printf("[auto_reservation] no fcm token user_id=%s", booking.UserID)
	}

	client, session, err := a.newSupabaseAuthClient(sessionRecord.SBRefreshToken)
	if err != nil {
		log.Printf("[auto_reservation] supabase refresh failed user_id=%s err=%v", booking.UserID, err)
		if err := a.setSysbookingSessionTokenValid(sessionRecord.UserID, false); err != nil {
			return err
		}
		if sendErr := a.notifyBookingTokenInvalid(sessionRecord, maid, timeSlotLabel); sendErr != nil {
			log.Printf("notify token invalid failed: user_id=%s err=%v", sessionRecord.UserID, sendErr)
		}
		return nil
	}

	payload := map[string]interface{}{
		"p_maid_vrcid":     strings.TrimSpace(maid.VRCID),
		"p_maid_name":      strings.TrimSpace(maid.Name),
		"p_guest_username": guestUsernameFromSession(session),
		"p_guest_user_id":  session.User.ID,
		"p_time_slot":      timeSlotLabel,
		"p_time":           formatZhCnNow(),
		"p_created_at":     time.Now().UTC().UnixMilli(),
		"p_with_friend":    booking.WithFriend,
		"p_friend_vrcid":   strings.TrimSpace(booking.FriendVRCID),
	}

	rpcResult := client.Rpc("add_reservation", "", payload)
	if rpcResultLooksLikeError(rpcResult) {
		log.Printf("[auto_reservation] rpc error booking_id=%s user_id=%s result=%s", booking.BookingID, booking.UserID, shortLogValue(rpcResult, 160))
		if isLikelySupabaseAuthError(rpcResult) {
			if err := a.setSysbookingSessionTokenValid(sessionRecord.UserID, false); err != nil {
				return err
			}
			if sendErr := a.notifyBookingTokenInvalid(sessionRecord, maid, timeSlotLabel); sendErr != nil {
				log.Printf("notify token invalid failed: user_id=%s err=%v", sessionRecord.UserID, sendErr)
			}
		}
		return fmt.Errorf("add_reservation failed: %s", rpcResult)
	}

	if err := a.markSysbookingBookingDone(booking.BookingID); err != nil {
		log.Printf("[auto_reservation] mark done failed booking_id=%s err=%v", booking.BookingID, err)
		return err
	}
	if sendErr := a.notifyBookingSuccess(sessionRecord, maid, timeSlotLabel); sendErr != nil {
		log.Printf("notify booking success failed: user_id=%s err=%v", sessionRecord.UserID, sendErr)
	}
	if booking.Autoqueue {
		if err := a.duplicateSysbookingBookingToTail(booking); err != nil {
			return err
		}
	}
	log.Printf("[auto_reservation] attempt ok booking_id=%s user_id=%s dur=%s", booking.BookingID, booking.UserID, time.Since(start))
	return nil
}

func normalizeReservationTimeSlot(raw string) (int, string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, "", false
	}
	value = strings.NewReplacer("，", ":").Replace(value)
	switch {
	case strings.HasPrefix(value, "21"):
		return 21, value, true
	case strings.HasPrefix(value, "22"):
		return 22, value, true
	default:
		return 0, "", false
	}
}

func uniqueStrings(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func rpcResultLooksLikeError(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		if _, ok := payload["message"]; ok {
			return true
		}
		if _, ok := payload["error"]; ok {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "jwt") {
		return true
	}
	if strings.Contains(lower, "unauthorized") {
		return true
	}
	if strings.Contains(lower, "invalid token") {
		return true
	}
	if strings.Contains(lower, "token expired") {
		return true
	}
	return false
}

func isLikelySupabaseAuthError(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "jwt") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid refresh token") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "token expired")
}

func formatZhCnNow() string {
	now := time.Now()
	two := func(n int) string {
		if n < 10 {
			return "0" + fmt.Sprint(n)
		}
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%d/%d/%d %s:%s:%s", now.Year(), now.Month(), now.Day(), two(now.Hour()), two(now.Minute()), two(now.Second()))
}
