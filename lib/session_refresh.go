package lib

import (
	"log"
	"time"
)

const dailyRefreshTokenHourUTC8 = 22

var utcPlus8 = time.FixedZone("UTC+8", 8*60*60)

func (a *App) startDailyRefreshTokenPoller() {
	for {
		wait := timeUntilNextDailyRefreshTokenCheck(time.Now())
		log.Printf("[poll.refresh] next run in %s", wait)
		timer := time.NewTimer(wait)
		<-timer.C
		timer.Stop()

		log.Printf("[poll.refresh] tick start")
		if err := a.runDailyRefreshTokenCheck(); err != nil {
			log.Printf("daily refresh token check failed: %v", err)
		} else {
			log.Printf("[poll.refresh] tick ok")
		}
	}
}

func timeUntilNextDailyRefreshTokenCheck(now time.Time) time.Duration {
	localNow := now.In(utcPlus8)
	next := time.Date(
		localNow.Year(),
		localNow.Month(),
		localNow.Day(),
		dailyRefreshTokenHourUTC8,
		0,
		0,
		0,
		utcPlus8,
	)
	if !next.After(localNow) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func (a *App) runDailyRefreshTokenCheck() error {
	start := time.Now()
	sessions, err := a.listLatestValidSysbookingSessions()
	if err != nil {
		log.Printf("[poll.refresh] list sessions failed err=%v", err)
		return err
	}
	log.Printf("[poll.refresh] loaded sessions count=%d", len(sessions))
	for _, session := range sessions {
		log.Printf("[poll.refresh] refresh session user_id=%s token_valid=%t notification_enabled=%t", session.UserID, session.TokenValid, session.NotificationEnabled)
		if err := a.refreshSysbookingSession(session); err != nil {
			log.Printf("refresh session failed: user_id=%s err=%v", session.UserID, err)
		}
	}
	log.Printf("[poll.refresh] tick done dur=%s", time.Since(start))
	return nil
}

func (a *App) listLatestValidSysbookingSessions() ([]sysbookingSessionRecord, error) {
	start := time.Now()
	rows, err := a.db.Query(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, notification_enabled, token_valid, token, updated_at
		 FROM sysbooking_sessions
		 WHERE token_valid = 1
		 ORDER BY user_id ASC, updated_at DESC, token DESC`,
	)
	if err != nil {
		log.Printf("[db] list sysbooking_sessions err=%v", err)
		return nil, err
	}
	defer rows.Close()

	seenUsers := make(map[string]struct{})
	sessions := make([]sysbookingSessionRecord, 0)
	for rows.Next() {
		var record sysbookingSessionRecord
		var updatedAt string
		var notificationEnabled int
		var tokenValid int
		if err := rows.Scan(&record.UserID, &record.SBRefreshToken, &record.SBToken, &record.FCMToken, &notificationEnabled, &tokenValid, &record.Token, &updatedAt); err != nil {
			return nil, err
		}
		if _, seen := seenUsers[record.UserID]; seen {
			continue
		}
		seenUsers[record.UserID] = struct{}{}
		record.NotificationEnabled = notificationEnabled == 1
		record.TokenValid = tokenValid == 1
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			record.UpdatedAt = t
		}
		sessions = append(sessions, record)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] list sysbooking_sessions rows err=%v", err)
		return nil, err
	}
	log.Printf("[db] list sysbooking_sessions count=%d dur=%s", len(sessions), time.Since(start))
	return sessions, nil
}

func (a *App) refreshSysbookingSession(session sysbookingSessionRecord) error {
	start := time.Now()
	_, _, valid, err := a.authenticatedSupabaseClient(session)
	if err != nil {
		log.Printf("[poll.refresh] token check failed user_id=%s err=%v", session.UserID, err)
		if err := a.setSysbookingSessionTokenValidByToken(session.Token, false); err != nil {
			return err
		}
		if err := a.notifySysbookingSessionRefreshInvalid(session, err.Error()); err != nil {
			log.Printf("notify refresh invalid failed: user_id=%s err=%v", session.UserID, err)
		}
		return nil
	}
	if !valid {
		log.Printf("[poll.refresh] token invalid after refresh user_id=%s", session.UserID)
		if err := a.setSysbookingSessionTokenValidByToken(session.Token, false); err != nil {
			return err
		}
		if err := a.notifySysbookingSessionRefreshInvalid(session, "access token remains invalid after refresh"); err != nil {
			log.Printf("notify refresh invalid failed: user_id=%s err=%v", session.UserID, err)
		}
		return nil
	}
	log.Printf("[poll.refresh] access token valid user_id=%s dur=%s", session.UserID, time.Since(start))
	return nil
}
