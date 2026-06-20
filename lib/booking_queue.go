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
	MaidID      string          `json:"maid_id"`
	Timeslot    bookingTimeslot `json:"timeslot"`
	Autoqueue   bool            `json:"autoqueue"`
	WithFriend  bool            `json:"with_friend"`
	FriendVRCID string          `json:"friend_vrcid"`
}

type sysbookingBookingDeleteRequest struct {
	BookingID string `json:"booking_id"`
}

type sysbookingBookingUpdateRequest struct {
	BookingID   string  `json:"booking_id"`
	Autoqueue   *bool   `json:"autoqueue"`
	WithFriend  *bool   `json:"with_friend"`
	FriendVRCID *string `json:"friend_vrcid"`
}

type sysbookingQueueListItem struct {
	BookingID   string `json:"booking_id"`
	MaidID      string `json:"maid_id"`
	WithFriend  bool   `json:"with_friend"`
	FriendVRCID string `json:"friend_vrcid"`
	Timeslot    int    `json:"timeslot"`
	Queue       int    `json:"queue"`
	Autoqueue   bool   `json:"autoqueue"`
}

type sysbookingBookingRecord struct {
	InternalID  int64
	BookingID   string
	UserID      string
	MaidID      string
	Timeslot    int
	Autoqueue   bool
	WithFriend  bool
	FriendVRCID string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (a *App) handleSysbookingBookingCreate(c *gin.Context) {
	log.Printf("[sysbooking.booking.create] request from=%s", c.ClientIP())
	var req sysbookingBookingCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[sysbooking.booking.create] invalid json err=%v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}
	maidID := strings.TrimSpace(req.MaidID)
	if maidID == "" {
		log.Printf("[sysbooking.booking.create] missing maid_id user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing maid_id"})
		return
	}
	if !req.Timeslot.valid() {
		log.Printf("[sysbooking.booking.create] invalid timeslot user_id=%s maid_id=%s timeslot=%v", userID, maidID, req.Timeslot)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid timeslot"})
		return
	}
	log.Printf("[sysbooking.booking.create] check occupancy user_id=%s maid_id=%s timeslot=%d", userID, maidID, req.Timeslot)
	hasQueue, err := a.hasWaitingSysbookingBooking(maidID, int(req.Timeslot))
	if err != nil {
		log.Printf("[sysbooking.booking.create] occupancy check failed user_id=%s maid_id=%s timeslot=%d err=%v", userID, maidID, req.Timeslot, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if hasQueue {
		log.Printf("[sysbooking.booking.create] slot occupied user_id=%s maid_id=%s timeslot=%d", userID, maidID, req.Timeslot)
		c.JSON(http.StatusConflict, gin.H{"error": "booking slot already occupied"})
		return
	}

	now := time.Now().UTC()
	record := sysbookingBookingRecord{
		BookingID:   generateBookingID(),
		UserID:      userID,
		MaidID:      maidID,
		Timeslot:    int(req.Timeslot),
		Autoqueue:   req.Autoqueue,
		WithFriend:  req.WithFriend,
		FriendVRCID: strings.TrimSpace(req.FriendVRCID),
		Status:      sysbookingBookingStatusWaiting,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := a.insertSysbookingBooking(record); err != nil {
		log.Printf("[sysbooking.booking.create] insert failed user_id=%s booking_id=%s err=%v", userID, record.BookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.booking.create] ok user_id=%s booking_id=%s maid_id=%s timeslot=%d autoqueue=%t with_friend=%t friend_vrcid=%q", userID, record.BookingID, maidID, record.Timeslot, record.Autoqueue, record.WithFriend, record.FriendVRCID)

	c.JSON(http.StatusCreated, gin.H{
		"booking_id": record.BookingID,
	})
}

func (a *App) handleSysbookingBookingDelete(c *gin.Context) {
	log.Printf("[sysbooking.booking.delete] request from=%s", c.ClientIP())
	var req sysbookingBookingDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[sysbooking.booking.delete] invalid json err=%v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}
	bookingID := strings.TrimSpace(req.BookingID)
	if bookingID == "" {
		log.Printf("[sysbooking.booking.delete] missing booking_id user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing booking_id"})
		return
	}

	record, err := a.getSysbookingBookingByBookingID(bookingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[sysbooking.booking.delete] not found booking_id=%s", bookingID)
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
		log.Printf("[sysbooking.booking.delete] lookup failed booking_id=%s err=%v", bookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if record.UserID != userID {
		log.Printf("[sysbooking.booking.delete] owner mismatch booking_id=%s owner=%s requester=%s", bookingID, record.UserID, userID)
		c.JSON(http.StatusForbidden, gin.H{"error": "booking owner mismatch"})
		return
	}
	if record.Status != sysbookingBookingStatusWaiting {
		log.Printf("[sysbooking.booking.delete] already closed booking_id=%s status=%s", bookingID, record.Status)
		c.JSON(http.StatusConflict, gin.H{"error": "booking already closed"})
		return
	}

	if err := a.cancelSysbookingBooking(record.BookingID, userID); err != nil {
		log.Printf("[sysbooking.booking.delete] cancel failed booking_id=%s err=%v", bookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.booking.delete] ok booking_id=%s user_id=%s", bookingID, userID)

	c.Status(http.StatusNoContent)
}

func (a *App) handleSysbookingQueueList(c *gin.Context) {
	log.Printf("[sysbooking.queuelist] request from=%s", c.ClientIP())
	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}

	items, err := a.listSysbookingQueueItems(userID)
	if err != nil {
		log.Printf("[sysbooking.queuelist] failed user_id=%s err=%v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.queuelist] ok user_id=%s count=%d", userID, len(items))

	c.JSON(http.StatusOK, items)
}

func (a *App) handleSysbookingBookingUpdate(c *gin.Context) {
	log.Printf("[sysbooking.booking.update] request from=%s", c.ClientIP())
	var req sysbookingBookingUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[sysbooking.booking.update] invalid json err=%v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	userID, err := a.requireSysbookingUserID(c)
	if err != nil {
		return
	}
	bookingID := strings.TrimSpace(req.BookingID)
	if bookingID == "" {
		log.Printf("[sysbooking.booking.update] missing booking_id user_id=%s", userID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing booking_id"})
		return
	}
	if req.Autoqueue == nil && req.WithFriend == nil && req.FriendVRCID == nil {
		log.Printf("[sysbooking.booking.update] missing update fields user_id=%s booking_id=%s", userID, bookingID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing autoqueue, with_friend or friend_vrcid"})
		return
	}

	record, err := a.getSysbookingBookingByBookingID(bookingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[sysbooking.booking.update] not found booking_id=%s", bookingID)
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
		log.Printf("[sysbooking.booking.update] lookup failed booking_id=%s err=%v", bookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if record.UserID != userID {
		log.Printf("[sysbooking.booking.update] owner mismatch booking_id=%s owner=%s requester=%s", bookingID, record.UserID, userID)
		c.JSON(http.StatusForbidden, gin.H{"error": "booking owner mismatch"})
		return
	}
	if record.Status != sysbookingBookingStatusWaiting {
		log.Printf("[sysbooking.booking.update] already closed booking_id=%s status=%s", bookingID, record.Status)
		c.JSON(http.StatusConflict, gin.H{"error": "booking already closed"})
		return
	}

	var friendVRCID *string
	if req.FriendVRCID != nil {
		trimmed := strings.TrimSpace(*req.FriendVRCID)
		friendVRCID = &trimmed
	}

	if err := a.updateSysbookingBookingFields(record.BookingID, req.Autoqueue, req.WithFriend, friendVRCID); err != nil {
		log.Printf("[sysbooking.booking.update] update failed booking_id=%s err=%v", bookingID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	logParts := make([]string, 0, 3)
	if req.Autoqueue != nil {
		logParts = append(logParts, fmt.Sprintf("autoqueue=%t", *req.Autoqueue))
	}
	if req.WithFriend != nil {
		logParts = append(logParts, fmt.Sprintf("with_friend=%t", *req.WithFriend))
	}
	if friendVRCID != nil {
		logParts = append(logParts, fmt.Sprintf("friend_vrcid=%q", *friendVRCID))
	}
	log.Printf("[sysbooking.booking.update] ok booking_id=%s %s", bookingID, strings.Join(logParts, " "))

	c.JSON(http.StatusOK, gin.H{"booking_id": record.BookingID})
}

func (a *App) handleSysbookingTokenValid(c *gin.Context) {
	log.Printf("[sysbooking.tokenvalid] request from=%s", c.ClientIP())
	token := strings.TrimSpace(c.GetHeader(sysbookingBookingTokenHeader))
	if token == "" {
		log.Printf("[sysbooking.tokenvalid] missing x-booking-token from=%s", c.ClientIP())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing x-booking-token"})
		return
	}
	session, err := a.getSysbookingSessionByToken(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[sysbooking.tokenvalid] invalid token=%s", shortLogValue(token, 12))
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid x-booking-token"})
			return
		}
		log.Printf("[sysbooking.tokenvalid] lookup failed token=%s err=%v", shortLogValue(token, 12), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	valid, err := a.isSupabaseAccessTokenValid(session.SBToken)
	if err != nil {
		log.Printf("[sysbooking.tokenvalid] supabase token check failed user_id=%s token=%s err=%v", session.UserID, shortLogValue(session.Token, 12), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[sysbooking.tokenvalid] ok user_id=%s token=%s sb_token_valid=%t", session.UserID, shortLogValue(session.Token, 12), valid)

	c.JSON(http.StatusOK, gin.H{
		"valid": valid,
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
		log.Printf("[sysbooking.auth] missing x-booking-token from=%s", c.ClientIP())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing x-booking-token"})
		return sysbookingSessionRecord{}, errors.New("missing x-booking-token")
	}
	log.Printf("[sysbooking.auth] lookup token=%s from=%s", shortLogValue(token, 12), c.ClientIP())
	session, err := a.getSysbookingSessionByToken(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("[sysbooking.auth] invalid token=%s", shortLogValue(token, 12))
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid x-booking-token"})
			return sysbookingSessionRecord{}, err
		}
		log.Printf("[sysbooking.auth] lookup failed token=%s err=%v", shortLogValue(token, 12), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return sysbookingSessionRecord{}, err
	}
	if !session.TokenValid {
		log.Printf("[sysbooking.auth] token disabled token=%s user_id=%s", shortLogValue(token, 12), session.UserID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid x-booking-token"})
		return sysbookingSessionRecord{}, sql.ErrNoRows
	}
	log.Printf("[sysbooking.auth] ok token=%s user_id=%s", shortLogValue(token, 12), session.UserID)
	return session, nil
}

func (a *App) getSysbookingSessionByToken(token string) (sysbookingSessionRecord, error) {
	start := time.Now()
	row := a.db.QueryRow(
		`SELECT user_id, sb_refreshtoken, sb_token, fcm_token, notification_enabled, token_valid, token, updated_at
		 FROM sysbooking_sessions WHERE token = ?`,
		strings.TrimSpace(token),
	)
	var record sysbookingSessionRecord
	var updatedAt string
	var notificationEnabled int
	var tokenValid int
	if err := row.Scan(&record.UserID, &record.SBRefreshToken, &record.SBToken, &record.FCMToken, &notificationEnabled, &tokenValid, &record.Token, &updatedAt); err != nil {
		log.Printf("[db] select sysbooking_sessions by token token=%s err=%v dur=%s", shortLogValue(token, 12), err, time.Since(start))
		return sysbookingSessionRecord{}, err
	}
	record.NotificationEnabled = notificationEnabled == 1
	record.TokenValid = tokenValid == 1
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	log.Printf("[db] select sysbooking_sessions by token token=%s user_id=%s token_valid=%t notification_enabled=%t fcm=%t dur=%s", shortLogValue(token, 12), record.UserID, record.TokenValid, record.NotificationEnabled, record.FCMToken.Valid, time.Since(start))
	return record, nil
}

func (a *App) insertSysbookingBooking(record sysbookingBookingRecord) error {
	start := time.Now()
	_, err := a.db.Exec(
		`INSERT INTO sysbooking_bookings (booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, friend_vrcid, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.BookingID,
		record.UserID,
		record.MaidID,
		record.Timeslot,
		boolToInt(record.Autoqueue),
		boolToInt(record.WithFriend),
		record.FriendVRCID,
		record.Status,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		log.Printf("[db] insert sysbooking_bookings booking_id=%s err=%v", record.BookingID, err)
		return err
	}
	log.Printf("[db] insert sysbooking_bookings booking_id=%s user_id=%s maid_id=%s timeslot=%d autoqueue=%t with_friend=%t friend_vrcid=%q dur=%s", record.BookingID, record.UserID, record.MaidID, record.Timeslot, record.Autoqueue, record.WithFriend, record.FriendVRCID, time.Since(start))
	return nil
}

func (a *App) hasWaitingSysbookingBooking(maidID string, timeslot int) (bool, error) {
	start := time.Now()
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
			log.Printf("[db] has waiting sysbooking_bookings maid_id=%s timeslot=%d exists=false dur=%s", maidID, timeslot, time.Since(start))
			return false, nil
		}
		log.Printf("[db] has waiting sysbooking_bookings maid_id=%s timeslot=%d err=%v dur=%s", maidID, timeslot, err, time.Since(start))
		return false, err
	}
	log.Printf("[db] has waiting sysbooking_bookings maid_id=%s timeslot=%d exists=true dur=%s", maidID, timeslot, time.Since(start))
	return true, nil
}

func (a *App) getWaitingSysbookingBookingHead(maidID string, timeslot int) (sysbookingBookingRecord, bool, error) {
	row := a.db.QueryRow(
		`SELECT id, booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, friend_vrcid, status, created_at, updated_at
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
	var friendVRCID string
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
		&friendVRCID,
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
	record.FriendVRCID = strings.TrimSpace(friendVRCID)
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		record.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	return record, true, nil
}

func (a *App) getSysbookingBookingByBookingID(bookingID string) (sysbookingBookingRecord, error) {
	start := time.Now()
	row := a.db.QueryRow(
		`SELECT id, booking_id, user_id, maid_id, timeslot, autoqueue, with_friend, friend_vrcid, status, created_at, updated_at
		 FROM sysbooking_bookings WHERE booking_id = ?`,
		strings.TrimSpace(bookingID),
	)
	var record sysbookingBookingRecord
	var autoqueue int
	var withFriend int
	var friendVRCID string
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
		&friendVRCID,
		&record.Status,
		&createdAt,
		&updatedAt,
	); err != nil {
		log.Printf("[db] select sysbooking_bookings booking_id=%s err=%v dur=%s", bookingID, err, time.Since(start))
		return sysbookingBookingRecord{}, err
	}
	record.Autoqueue = autoqueue == 1
	record.WithFriend = withFriend == 1
	record.FriendVRCID = strings.TrimSpace(friendVRCID)
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		record.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		record.UpdatedAt = t
	}
	log.Printf("[db] select sysbooking_bookings booking_id=%s user_id=%s status=%s autoqueue=%t dur=%s", bookingID, record.UserID, record.Status, record.Autoqueue, time.Since(start))
	return record, nil
}

func (a *App) cancelSysbookingBooking(bookingID string, userID string) error {
	start := time.Now()
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
	if err != nil {
		log.Printf("[db] cancel sysbooking_bookings booking_id=%s user_id=%s err=%v", bookingID, userID, err)
		return err
	}
	log.Printf("[db] cancel sysbooking_bookings booking_id=%s user_id=%s dur=%s", bookingID, userID, time.Since(start))
	return nil
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

func (a *App) updateSysbookingBookingFields(bookingID string, autoqueue *bool, withFriend *bool, friendVRCID *string) error {
	if autoqueue == nil && withFriend == nil && friendVRCID == nil {
		return errors.New("missing update fields")
	}
	start := time.Now()
	sets := make([]string, 0, 3)
	args := make([]interface{}, 0, 4)
	if autoqueue != nil {
		sets = append(sets, "autoqueue = ?")
		args = append(args, boolToInt(*autoqueue))
	}
	if withFriend != nil {
		sets = append(sets, "with_friend = ?")
		args = append(args, boolToInt(*withFriend))
	}
	if friendVRCID != nil {
		sets = append(sets, "friend_vrcid = ?")
		args = append(args, strings.TrimSpace(*friendVRCID))
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339Nano))
	args = append(args, strings.TrimSpace(bookingID), sysbookingBookingStatusWaiting)

	_, err := a.db.Exec(
		fmt.Sprintf(
			`UPDATE sysbooking_bookings
		 SET %s
		 WHERE booking_id = ? AND status = ?`,
			strings.Join(sets, ", "),
		),
		args...,
	)
	if err != nil {
		log.Printf("[db] update sysbooking_bookings booking_id=%s err=%v", bookingID, err)
		return err
	}
	log.Printf("[db] update sysbooking_bookings booking_id=%s autoqueue_set=%t with_friend_set=%t friend_vrcid_set=%t dur=%s", bookingID, autoqueue != nil, withFriend != nil, friendVRCID != nil, time.Since(start))
	return nil
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
	return a.getSysbookingQueueRankForBooking(maidID, timeslot, targetCreatedAt, targetID)
}

func (a *App) getSysbookingQueueRankForBooking(maidID string, timeslot int, createdAt string, bookingID int64) (int, bool, error) {
	targetCreatedAt := strings.TrimSpace(createdAt)
	if targetCreatedAt == "" || bookingID == 0 {
		return 0, false, nil
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
		bookingID,
	)
	var rank int
	if err := countRow.Scan(&rank); err != nil {
		return 0, false, err
	}
	return rank, true, nil
}

func (a *App) listSysbookingQueueItems(userID string) ([]sysbookingQueueListItem, error) {
	start := time.Now()
	rows, err := a.db.Query(
		`SELECT b.id, b.booking_id, b.maid_id,
		        b.with_friend, b.friend_vrcid, b.timeslot, b.autoqueue, b.created_at
		 FROM sysbooking_bookings b
		 WHERE b.user_id = ? AND b.status = ?
		 ORDER BY b.maid_id ASC, b.timeslot ASC, b.created_at ASC, b.id ASC`,
		strings.TrimSpace(userID),
		sysbookingBookingStatusWaiting,
	)
	if err != nil {
		log.Printf("[db] list sysbooking queue items user_id=%s err=%v", userID, err)
		return nil, err
	}
	defer rows.Close()

	type queueRow struct {
		internalID  int64
		bookingID   string
		maidID      string
		withFriend  int
		friendVRCID string
		timeslot    int
		autoqueue   int
		createdAt   string
	}

	rawRows := make([]queueRow, 0)
	for rows.Next() {
		var item queueRow
		if err := rows.Scan(&item.internalID, &item.bookingID, &item.maidID, &item.withFriend, &item.friendVRCID, &item.timeslot, &item.autoqueue, &item.createdAt); err != nil {
			log.Printf("[db] list sysbooking queue items user_id=%s scan err=%v", userID, err)
			return nil, err
		}
		rawRows = append(rawRows, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] list sysbooking queue items user_id=%s rows err=%v", userID, err)
		return nil, err
	}

	items := make([]sysbookingQueueListItem, 0)
	for _, row := range rawRows {
		rank, ok, err := a.getSysbookingQueueRankForBooking(row.maidID, row.timeslot, row.createdAt, row.internalID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		items = append(items, sysbookingQueueListItem{
			BookingID:   row.bookingID,
			MaidID:      row.maidID,
			WithFriend:  row.withFriend == 1,
			FriendVRCID: row.friendVRCID,
			Timeslot:    row.timeslot,
			Queue:       rank,
			Autoqueue:   row.autoqueue == 1,
		})
	}
	log.Printf("[db] list sysbooking queue items user_id=%s count=%d dur=%s", userID, len(items), time.Since(start))
	return items, nil
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
