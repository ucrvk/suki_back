package lib

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/supabase-community/supabase-go"
)

type sysbookingLoginRequest struct {
	UserID         string  `json:"user_id"`
	SBRefreshToken string  `json:"sb_refreshtoken"`
	FCMToken       *string `json:"fcm_token,omitempty"`
}

type sysbookingNotificationUpdateRequest struct {
	FCMToken     string `json:"fcm_token"`
	Notification *bool  `json:"notification"`
}

type supabaseRefreshSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         struct {
		ID           string                 `json:"id"`
		Aud          string                 `json:"aud"`
		Role         string                 `json:"role"`
		Email        string                 `json:"email"`
		AppMetadata  map[string]interface{} `json:"app_metadata"`
		UserMetadata map[string]interface{} `json:"user_metadata"`
	} `json:"user"`
}

type sysbookingSessionRecord struct {
	UserID              string
	SBRefreshToken      string
	SBToken             string
	FCMToken            sql.NullString
	NotificationEnabled bool
	TokenValid          bool
	Token               string
	UpdatedAt           time.Time
}

func (a *App) handleSysbookingLogin(c *gin.Context) {
	log.Printf("[sysbooking.login] request from=%s", c.ClientIP())
	var req sysbookingLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[sysbooking.login] invalid json err=%v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID := strings.TrimSpace(req.UserID)
	refreshToken := strings.TrimSpace(req.SBRefreshToken)
	if userID == "" {
		log.Printf("[sysbooking.login] missing user_id")
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing user_id"})
		return
	}
	if refreshToken == "" {
		log.Printf("[sysbooking.login] missing sb_refreshtoken user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing sb_refreshtoken"})
		return
	}

	log.Printf("[sysbooking.login] refreshing supabase user_id=%s", userID)
	_, session, err := a.newSupabaseAuthClient(refreshToken)
	if err != nil {
		log.Printf("[sysbooking.login] refresh failed user_id=%s err=%v", userID, err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(session.User.ID) != userID {
		log.Printf("[sysbooking.login] user mismatch request_user_id=%s session_user_id=%s", userID, strings.TrimSpace(session.User.ID))
		c.JSON(http.StatusForbidden, gin.H{"error": "user id mismatch"})
		return
	}

	localToken, err := generateLocalToken64()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate token failed"})
		return
	}

	record := sysbookingSessionRecord{
		UserID:         userID,
		SBRefreshToken: firstNonEmpty(strings.TrimSpace(session.RefreshToken), refreshToken),
		SBToken:        strings.TrimSpace(session.AccessToken),
		FCMToken:       optionalStringToNullString(req.FCMToken),
		TokenValid:     true,
		Token:          localToken,
		UpdatedAt:      time.Now().UTC(),
	}
	if err := a.upsertSysbookingSession(record); err != nil {
		log.Printf("[sysbooking.login] store failed user_id=%s err=%v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.login] ok user_id=%s token=%s fcm=%t notification_enabled=%t", userID, shortLogValue(localToken, 12), record.FCMToken.Valid, record.NotificationEnabled)

	c.JSON(http.StatusOK, gin.H{
		"user_id": userID,
		"token":   localToken,
	})
}

func (a *App) handleSysbookingNotificationUpdate(c *gin.Context) {
	log.Printf("[sysbooking.notification] request from=%s", c.ClientIP())
	var req sysbookingNotificationUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[sysbooking.notification] invalid json err=%v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}

	fcmToken := strings.TrimSpace(req.FCMToken)
	if fcmToken == "" {
		log.Printf("[sysbooking.notification] missing fcm_token user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing fcm_token"})
		return
	}
	if req.Notification == nil {
		log.Printf("[sysbooking.notification] missing notification user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing notification"})
		return
	}

	log.Printf("[sysbooking.notification] update user_id=%s fcm=%s notification=%t", userID, shortLogValue(fcmToken, 12), *req.Notification)
	if err := a.updateSysbookingSessionNotification(userID, fcmToken, *req.Notification); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[sysbooking.notification] session not found user_id=%s", userID)
			c.JSON(http.StatusNotFound, gin.H{"error": "sysbooking session not found"})
			return
		}
		log.Printf("[sysbooking.notification] store failed user_id=%s err=%v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.notification] ok user_id=%s notification=%t", userID, *req.Notification)

	c.JSON(http.StatusOK, gin.H{
		"user_id":              userID,
		"fcm_token":            fcmToken,
		"notification_enabled": *req.Notification,
	})
}

func (a *App) refreshSupabaseSession(refreshToken string) (supabaseRefreshSession, error) {
	_, session, err := a.newSupabaseAuthClient(refreshToken)
	if err != nil {
		return supabaseRefreshSession{}, err
	}
	return session, nil
}

func (a *App) newSupabaseAuthClient(refreshToken string) (*supabase.Client, supabaseRefreshSession, error) {
	client, err := supabase.NewClient(a.cfg.SupabaseURL, a.cfg.SupabaseKey, &supabase.ClientOptions{
		Schema: "public",
		Headers: map[string]string{
			"User-Agent": supabaseUserAgent,
		},
	})
	if err != nil {
		return nil, supabaseRefreshSession{}, err
	}
	session, err := client.RefreshToken(refreshToken)
	if err != nil {
		return nil, supabaseRefreshSession{}, err
	}
	sessionInfo := supabaseRefreshSession{
		AccessToken:  session.AccessToken,
		RefreshToken: session.RefreshToken,
		TokenType:    session.TokenType,
		ExpiresIn:    session.ExpiresIn,
		ExpiresAt:    session.ExpiresAt,
		User: struct {
			ID           string                 `json:"id"`
			Aud          string                 `json:"aud"`
			Role         string                 `json:"role"`
			Email        string                 `json:"email"`
			AppMetadata  map[string]interface{} `json:"app_metadata"`
			UserMetadata map[string]interface{} `json:"user_metadata"`
		}{
			ID:           session.User.ID.String(),
			Aud:          session.User.Aud,
			Role:         session.User.Role,
			Email:        session.User.Email,
			AppMetadata:  session.User.AppMetadata,
			UserMetadata: session.User.UserMetadata,
		},
	}
	return client, sessionInfo, nil
}

func generateLocalToken64() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (a *App) upsertSysbookingSession(record sysbookingSessionRecord) error {
	start := time.Now()
	_, err := a.db.Exec(
		`INSERT INTO sysbooking_sessions (user_id, sb_refreshtoken, sb_token, fcm_token, token_valid, token, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   sb_refreshtoken=excluded.sb_refreshtoken,
		   sb_token=excluded.sb_token,
		   fcm_token=COALESCE(excluded.fcm_token, sysbooking_sessions.fcm_token),
		   token_valid=excluded.token_valid,
		   token=excluded.token,
		   updated_at=excluded.updated_at`,
		record.UserID,
		record.SBRefreshToken,
		record.SBToken,
		record.FCMToken,
		boolToInt(record.TokenValid),
		record.Token,
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		log.Printf("[db] upsert sysbooking_sessions user_id=%s err=%v", record.UserID, err)
		return err
	}
	log.Printf("[db] upsert sysbooking_sessions user_id=%s fcm=%t notification_enabled=%t token_valid=%t dur=%s", record.UserID, record.FCMToken.Valid, record.NotificationEnabled, record.TokenValid, time.Since(start))
	return nil
}

func (a *App) updateSysbookingSessionNotification(userID, fcmToken string, enabled bool) error {
	start := time.Now()
	result, err := a.db.Exec(
		`UPDATE sysbooking_sessions
		 SET fcm_token = ?, notification_enabled = ?, updated_at = ?
		 WHERE user_id = ?`,
		strings.TrimSpace(fcmToken),
		boolToInt(enabled),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(userID),
	)
	if err != nil {
		log.Printf("[db] update sysbooking_sessions notification user_id=%s err=%v", userID, err)
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("[db] update sysbooking_sessions notification user_id=%s rows_affected err=%v", userID, err)
		return err
	}
	if rowsAffected == 0 {
		log.Printf("[db] update sysbooking_sessions notification user_id=%s not found", userID)
		return sql.ErrNoRows
	}
	log.Printf("[db] update sysbooking_sessions notification user_id=%s fcm=%s enabled=%t dur=%s", userID, shortLogValue(fcmToken, 12), enabled, time.Since(start))
	return nil
}

func (a *App) getSysbookingSession(userID string) (sysbookingSessionRecord, error) {
	start := time.Now()
	row := a.db.QueryRow(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, notification_enabled, token_valid, token, updated_at
		 FROM sysbooking_sessions WHERE user_id = ?`,
		userID,
	)
	var record sysbookingSessionRecord
	var updatedAt string
	var notificationEnabled int
	var tokenValid int
	if err := row.Scan(&record.UserID, &record.SBRefreshToken, &record.SBToken, &record.FCMToken, &notificationEnabled, &tokenValid, &record.Token, &updatedAt); err != nil {
		log.Printf("[db] select sysbooking_sessions user_id=%s err=%v dur=%s", userID, err, time.Since(start))
		return sysbookingSessionRecord{}, err
	}
	record.NotificationEnabled = notificationEnabled == 1
	record.TokenValid = tokenValid == 1
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	log.Printf("[db] select sysbooking_sessions user_id=%s found token_valid=%t notification_enabled=%t fcm=%t dur=%s", userID, record.TokenValid, record.NotificationEnabled, record.FCMToken.Valid, time.Since(start))
	return record, nil
}

func (a *App) hasSysbookingSession(userID string) (bool, error) {
	_, err := a.getSysbookingSession(userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *App) setSysbookingSessionTokenValid(userID string, valid bool) error {
	start := time.Now()
	_, err := a.db.Exec(
		`UPDATE sysbooking_sessions
		 SET token_valid = ?, updated_at = ?
		 WHERE user_id = ?`,
		boolToInt(valid),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(userID),
	)
	if err != nil {
		log.Printf("[db] update sysbooking_sessions token_valid user_id=%s valid=%t err=%v", userID, valid, err)
		return err
	}
	log.Printf("[db] update sysbooking_sessions token_valid user_id=%s valid=%t dur=%s", userID, valid, time.Since(start))
	return err
}

func guestUsernameFromSession(session supabaseRefreshSession) string {
	if session.User.UserMetadata != nil {
		if raw, ok := session.User.UserMetadata["username"]; ok {
			if value := strings.TrimSpace(fmt.Sprint(raw)); value != "" {
				return value
			}
		}
	}
	email := strings.TrimSpace(session.User.Email)
	if idx := strings.IndexByte(email, '@'); idx > 0 {
		if value := strings.TrimSpace(email[:idx]); value != "" {
			return value
		}
	}
	if email != "" {
		return email
	}
	return strings.TrimSpace(session.User.ID)
}

func optionalStringToNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: trimmed, Valid: true}
}
