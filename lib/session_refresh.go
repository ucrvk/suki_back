package lib

import (
	"fmt"
	"log"
	"strings"
	"time"
)

const dailyRefreshTokenHourUTC8 = 22

var utcPlus8 = time.FixedZone("UTC+8", 8*60*60)

func (a *App) startDailyRefreshTokenPoller() {
	for {
		wait := timeUntilNextDailyRefreshTokenCheck(time.Now())
		timer := time.NewTimer(wait)
		<-timer.C
		timer.Stop()

		if err := a.runDailyRefreshTokenCheck(); err != nil {
			log.Printf("daily refresh token check failed: %v", err)
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
	sessions, err := a.listSysbookingSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if err := a.refreshSysbookingSession(session); err != nil {
			log.Printf("refresh session failed: user_id=%s err=%v", session.UserID, err)
		}
	}
	return nil
}

func (a *App) listSysbookingSessions() ([]sysbookingSessionRecord, error) {
	rows, err := a.db.Query(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, token_valid, token, updated_at
		 FROM sysbooking_sessions`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]sysbookingSessionRecord, 0)
	for rows.Next() {
		var record sysbookingSessionRecord
		var updatedAt string
		var tokenValid int
		if err := rows.Scan(&record.UserID, &record.SBRefreshToken, &record.SBToken, &record.FCMToken, &tokenValid, &record.Token, &updatedAt); err != nil {
			return nil, err
		}
		record.TokenValid = tokenValid == 1
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			record.UpdatedAt = t
		}
		sessions = append(sessions, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (a *App) refreshSysbookingSession(session sysbookingSessionRecord) error {
	refreshToken := strings.TrimSpace(session.SBRefreshToken)
	if refreshToken == "" {
		if err := a.setSysbookingSessionTokenValid(session.UserID, false); err != nil {
			return err
		}
		if err := a.notifySysbookingSessionRefreshInvalid(session, "missing refresh token"); err != nil {
			log.Printf("notify refresh invalid failed: user_id=%s err=%v", session.UserID, err)
		}
		return nil
	}

	_, refreshed, err := a.newSupabaseAuthClient(refreshToken)
	if err != nil {
		if err := a.setSysbookingSessionTokenValid(session.UserID, false); err != nil {
			return err
		}
		if err := a.notifySysbookingSessionRefreshInvalid(session, err.Error()); err != nil {
			log.Printf("notify refresh invalid failed: user_id=%s err=%v", session.UserID, err)
		}
		return nil
	}
	if refreshed.User.ID != "" && refreshed.User.ID != session.UserID {
		if err := a.setSysbookingSessionTokenValid(session.UserID, false); err != nil {
			return err
		}
		if err := a.notifySysbookingSessionRefreshInvalid(session, fmt.Sprintf("user id mismatch: %s", refreshed.User.ID)); err != nil {
			log.Printf("notify refresh invalid failed: user_id=%s err=%v", session.UserID, err)
		}
		return nil
	}

	updated := session
	updated.SBRefreshToken = firstNonEmpty(strings.TrimSpace(refreshed.RefreshToken), refreshToken)
	updated.SBToken = strings.TrimSpace(refreshed.AccessToken)
	updated.TokenValid = true
	updated.UpdatedAt = time.Now().UTC()
	if err := a.upsertSysbookingSession(updated); err != nil {
		return err
	}
	return nil
}
