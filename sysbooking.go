package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/supabase-community/supabase-go"
)

type sysbookingLoginRequest struct {
	ID             string  `json:"id"`
	SBRefreshToken string  `json:"sb_refreshtoken"`
	FCMToken       *string `json:"fcm_token,omitempty"`
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
	UserID         string
	SBRefreshToken string
	SBToken        string
	FCMToken       sql.NullString
	TokenValid     bool
	Token          string
	UpdatedAt      time.Time
}

func (a *App) handleSysbookingLogin(c *gin.Context) {
	var req sysbookingLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID := strings.TrimSpace(req.ID)
	refreshToken := strings.TrimSpace(req.SBRefreshToken)
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	if refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing sb_refreshtoken"})
		return
	}

	_, session, err := a.newSupabaseAuthClient(refreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(session.User.ID) != userID {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id": userID,
		"token":   localToken,
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
	return err
}

func (a *App) getSysbookingSession(userID string) (sysbookingSessionRecord, error) {
	row := a.db.QueryRow(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, token_valid, token, updated_at
		 FROM sysbooking_sessions WHERE user_id = ?`,
		userID,
	)
	var record sysbookingSessionRecord
	var updatedAt string
	var tokenValid int
	if err := row.Scan(&record.UserID, &record.SBRefreshToken, &record.SBToken, &record.FCMToken, &tokenValid, &record.Token, &updatedAt); err != nil {
		return sysbookingSessionRecord{}, err
	}
	record.TokenValid = tokenValid == 1
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
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
	_, err := a.db.Exec(
		`UPDATE sysbooking_sessions
		 SET token_valid = ?, updated_at = ?
		 WHERE user_id = ?`,
		boolToInt(valid),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(userID),
	)
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
