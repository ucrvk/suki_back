package lib

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
)

const (
	sysbookingBookingStatusWaiting   = "waiting"
	sysbookingBookingStatusCancelled = "cancelled"
	sysbookingBookingStatusDone      = "done"
	sysbookingBookingTokenHeader     = "x-booking-token"
)

type bookingTimeslot int

func (t *bookingTimeslot) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return errors.New("missing timeslot")
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = strings.TrimSpace(raw[1 : len(raw)-1])
	}
	switch raw {
	case "21":
		*t = 21
		return nil
	case "22":
		*t = 22
		return nil
	default:
		return fmt.Errorf("invalid timeslot: %s", raw)
	}
}

func (t bookingTimeslot) valid() bool {
	return t == 21 || t == 22
}

type sysbookingBookingCreateRequest struct {
	MaidID     string          `json:"maid_id"`
	Timeslot   bookingTimeslot `json:"timeslot"`
	Autoqueue  bool            `json:"autoqueue"`
	WithFriend bool            `json:"with_friend"`
}

type sysbookingBookingDeleteRequest struct {
	BookingID string `json:"booking_id"`
}

type sysbookingQueueQueryRequest struct {
	MaidID   string
	Timeslot bookingTimeslot
}

type sysbookingBookingRecord struct {
	InternalID int64
	BookingID  string
	UserID     string
	MaidID     string
	Timeslot   int
	Autoqueue  bool
	WithFriend bool
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (a *App) handleSysbookingBookingCreate(c *gin.Context) {
	var req sysbookingBookingCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}
	maidID := strings.TrimSpace(req.MaidID)
	if maidID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing maid_id"})
		return
	}
	if !req.Timeslot.valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid timeslot"})
		return
	}
	hasQueue, err := a.hasWaitingSysbookingBooking(maidID, int(req.Timeslot))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if hasQueue {
		c.JSON(http.StatusConflict, gin.H{"error": "booking slot already occupied"})
		return
	}

	now := time.Now().UTC()
	record := sysbookingBookingRecord{
		BookingID:  generateBookingID(),
		UserID:     userID,
		MaidID:     maidID,
		Timeslot:   int(req.Timeslot),
		Autoqueue:  req.Autoqueue,
		WithFriend: req.WithFriend,
		Status:     sysbookingBookingStatusWaiting,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := a.insertSysbookingBooking(record); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"booking_id": record.BookingID,
	})
}

func (a *App) handleSysbookingBookingDelete(c *gin.Context) {
	var req sysbookingBookingDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}
	bookingID := strings.TrimSpace(req.BookingID)
	if bookingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing booking_id"})
		return
	}

	record, err := a.getSysbookingBookingByBookingID(bookingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if record.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "booking owner mismatch"})
		return
	}
	if record.Status != sysbookingBookingStatusWaiting {
		c.JSON(http.StatusConflict, gin.H{"error": "booking already closed"})
		return
	}

	if err := a.cancelSysbookingBooking(record.BookingID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

func (a *App) handleSysbookingQueueQuery(c *gin.Context) {
	req := sysbookingQueueQueryRequest{
		MaidID: strings.TrimSpace(c.Query("maidid")),
	}
	if req.MaidID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing maidid"})
		return
	}
	timeslotRaw := strings.TrimSpace(c.Query("timeslot"))
	switch timeslotRaw {
	case "21":
		req.Timeslot = 21
	case "22":
		req.Timeslot = 22
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid timeslot"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}

	rank, ok, err := a.getSysbookingQueueRank(userID, req.MaidID, int(req.Timeslot))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, rank)
}

func (a *App) handleSysbookingTokenValid(c *gin.Context) {
	session, err := a.requireSysbookingSession(c)
	if err != nil {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"valid": session.TokenValid,
	})
}

func (a *App) requireSysbookingUserID(c *gin.Context) (string, error) {
	session, err := a.requireSysbookingSession(c)
	if err != nil {
		return "", err
	}
	return session.UserID, nil
}

func (a *App) requireSysbookingSession(c *gin.Context) (sysbookingSessionRecord, error) {
	token := strings.TrimSpace(c.GetHeader(sysbookingBookingTokenHeader))
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing x-booking-token"})
		return sysbookingSessionRecord{}, errors.New("missing x-booking-token")
	}
	session, err := a.getSysbookingSessionByToken(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid x-booking-token"})
			return sysbookingSessionRecord{}, err
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return sysbookingSessionRecord{}, err
	}
	return session, nil
}

func (a *App) getSysbookingSessionByToken(token string) (sysbookingSessionRecord, error) {
	row := a.db.QueryRow(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, token_valid, token, updated_at
		 FROM sysbooking_sessions WHERE token = ?`,
		strings.TrimSpace(token),
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

func (a *App) insertSysbookingBooking(record sysbookingBookingRecord) error {
	_, err := a.db.Exec(
		`INSERT INTO sysbooking_bookings (booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.BookingID,
		record.UserID,
		record.MaidID,
		record.Timeslot,
		boolToInt(record.Autoqueue),
		boolToInt(record.WithFriend),
		record.Status,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (a *App) hasWaitingSysbookingBooking(maidID string, timeslot int) (bool, error) {
	row := a.db.QueryRow(
		`SELECT 1
		 FROM sysbooking_bookings
		 WHERE maid_id = ? AND timeslot = ? AND status = ?
		 LIMIT 1`,
		strings.TrimSpace(maidID),
		timeslot,
		sysbookingBookingStatusWaiting,
	)
	var exists int
	if err := row.Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *App) getWaitingSysbookingBookingHead(maidID string, timeslot int) (sysbookingBookingRecord, bool, error) {
	row := a.db.QueryRow(
		`SELECT id, booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, status, created_at, updated_at
		 FROM sysbooking_bookings
		 WHERE maid_id = ? AND timeslot = ? AND status = ?
		 ORDER BY created_at ASC, id ASC
		 LIMIT 1`,
		strings.TrimSpace(maidID),
		timeslot,
		sysbookingBookingStatusWaiting,
	)
	var record sysbookingBookingRecord
	var autoqueue int
	var withFriend int
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&record.InternalID,
		&record.BookingID,
		&record.UserID,
		&record.MaidID,
		&record.Timeslot,
		&autoqueue,
		&withFriend,
		&record.Status,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sysbookingBookingRecord{}, false, nil
		}
		return sysbookingBookingRecord{}, false, err
	}
	record.Autoqueue = autoqueue == 1
	record.WithFriend = withFriend == 1
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		record.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	return record, true, nil
}

func (a *App) getSysbookingBookingByBookingID(bookingID string) (sysbookingBookingRecord, error) {
	row := a.db.QueryRow(
		`SELECT id, booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, status, created_at, updated_at
		 FROM sysbooking_bookings WHERE booking_id = ?`,
		strings.TrimSpace(bookingID),
	)
	var record sysbookingBookingRecord
	var autoqueue int
	var withFriend int
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&record.InternalID,
		&record.BookingID,
		&record.UserID,
		&record.MaidID,
		&record.Timeslot,
		&autoqueue,
		&withFriend,
		&record.Status,
		&createdAt,
		&updatedAt,
	); err != nil {
		return sysbookingBookingRecord{}, err
	}
	record.Autoqueue = autoqueue == 1
	record.WithFriend = withFriend == 1
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		record.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	return record, nil
}

func (a *App) cancelSysbookingBooking(bookingID string, userID string) error {
	_, err := a.db.Exec(
		`UPDATE sysbooking_bookings
		 SET status = ?, updated_at = ?
		 WHERE booking_id = ? AND user_id = ? AND status = ?`,
		sysbookingBookingStatusCancelled,
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(bookingID),
		strings.TrimSpace(userID),
		sysbookingBookingStatusWaiting,
	)
	return err
}

func (a *App) markSysbookingBookingDone(bookingID string) error {
	_, err := a.db.Exec(
		`UPDATE sysbooking_bookings
		 SET status = ?, updated_at = ?
		 WHERE booking_id = ? AND status = ?`,
		sysbookingBookingStatusDone,
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(bookingID),
		sysbookingBookingStatusWaiting,
	)
	return err
}

func (a *App) duplicateSysbookingBookingToTail(record sysbookingBookingRecord) error {
	clone := record
	clone.InternalID = 0
	clone.BookingID = generateBookingID()
	clone.Status = sysbookingBookingStatusWaiting
	now := time.Now().UTC()
	clone.CreatedAt = now
	clone.UpdatedAt = now
	return a.insertSysbookingBooking(clone)
}

func (a *App) getSysbookingQueueRank(userID, maidID string, timeslot int) (int, bool, error) {
	row := a.db.QueryRow(
		`SELECT id, created_at
		 FROM sysbooking_bookings
		 WHERE user_id = ? AND maid_id = ? AND timeslot = ? AND status = ?
		 ORDER BY created_at ASC, id ASC
		 LIMIT 1`,
		strings.TrimSpace(userID),
		strings.TrimSpace(maidID),
		timeslot,
		sysbookingBookingStatusWaiting,
	)
	var targetID int64
	var targetCreatedAt string
	if err := row.Scan(&targetID, &targetCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}

	countRow := a.db.QueryRow(
		`SELECT COUNT(*)
		 FROM sysbooking_bookings
		 WHERE maid_id = ?
		   AND timeslot = ?
		   AND status = ?
		   AND (
			 created_at < ?
			 OR (created_at = ? AND id < ?)
		   )`,
		strings.TrimSpace(maidID),
		timeslot,
		sysbookingBookingStatusWaiting,
		targetCreatedAt,
		targetCreatedAt,
		targetID,
	)
	var rank int
	if err := countRow.Scan(&rank); err != nil {
		return 0, false, err
	}
	return rank, true, nil
}

func generateBookingID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
