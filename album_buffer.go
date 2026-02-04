package main

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// AlbumMessage represents a single media item in an album
type AlbumMessage struct {
	ID       string                 `json:"id"`
	URL      string                 `json:"url,omitempty"`
	Base64   string                 `json:"base64,omitempty"`
	MimeType string                 `json:"mimeType,omitempty"`
	FileName string                 `json:"fileName,omitempty"`
	S3       map[string]interface{} `json:"s3,omitempty"`
}

// AlbumData holds all information about a pending album
type AlbumData struct {
	AlbumID   string
	ChatJID   string
	SenderJID string
	SenderAlt string
	Caption   string
	Timestamp time.Time
	Messages  []AlbumMessage
	Timer     *time.Timer
	UserID    string
	Token     string
	MyCli     *MyClient
}

// AlbumWebhookPayload is the structure sent to webhook when album is complete
type AlbumWebhookPayload struct {
	Type        string         `json:"type"`
	AlbumID     string         `json:"albumId"`
	Sender      string         `json:"sender"`
	SenderLid   string         `json:"senderLid,omitempty"`
	Chat        string         `json:"chat"`
	Caption     string         `json:"caption,omitempty"`
	Timestamp   string         `json:"timestamp"`
	TotalImages int            `json:"totalImages"`
	Images      []AlbumMessage `json:"images"`
}

// AlbumBuffer manages pending albums waiting to be aggregated
type AlbumBuffer struct {
	sync.RWMutex
	albums      map[string]*AlbumData // key: albumId (parentMessageKey)
	waitSeconds int
	enabled     bool
}

// Global album buffer instance
var albumBuffer *AlbumBuffer

// InitAlbumBuffer initializes the global album buffer
func InitAlbumBuffer(waitSeconds int, enabled bool) {
	albumBuffer = &AlbumBuffer{
		albums:      make(map[string]*AlbumData),
		waitSeconds: waitSeconds,
		enabled:     enabled,
	}
	log.Info().
		Int("waitSeconds", waitSeconds).
		Bool("enabled", enabled).
		Msg("Album buffer initialized")
}

// GetAlbumBuffer returns the global album buffer instance
func GetAlbumBuffer() *AlbumBuffer {
	return albumBuffer
}

// IsEnabled returns whether album grouping is enabled
func (ab *AlbumBuffer) IsEnabled() bool {
	return ab.enabled
}

// AddMessage adds a message to an album buffer
// Returns true if this is the first message in the album
func (ab *AlbumBuffer) AddMessage(albumID string, msg AlbumMessage, metadata *AlbumData) bool {
	ab.Lock()
	defer ab.Unlock()

	isFirst := false

	album, exists := ab.albums[albumID]
	if !exists {
		// First message in this album
		isFirst = true
		album = &AlbumData{
			AlbumID:   albumID,
			ChatJID:   metadata.ChatJID,
			SenderJID: metadata.SenderJID,
			SenderAlt: metadata.SenderAlt,
			Caption:   metadata.Caption,
			Timestamp: metadata.Timestamp,
			Messages:  []AlbumMessage{},
			UserID:    metadata.UserID,
			Token:     metadata.Token,
			MyCli:     metadata.MyCli,
		}
		ab.albums[albumID] = album

		// Start timer for this album
		album.Timer = time.AfterFunc(time.Duration(ab.waitSeconds)*time.Second, func() {
			ab.flushAlbum(albumID)
		})

		log.Info().
			Str("albumId", albumID).
			Str("chat", metadata.ChatJID).
			Int("waitSeconds", ab.waitSeconds).
			Msg("New album detected, starting buffer timer")
	} else {
		// Subsequent message - update caption if this one has it and previous didn't
		if album.Caption == "" && metadata.Caption != "" {
			album.Caption = metadata.Caption
		}

		// Reset timer since we got a new message
		if album.Timer != nil {
			album.Timer.Reset(time.Duration(ab.waitSeconds) * time.Second)
		}
	}

	// Add message to album
	album.Messages = append(album.Messages, msg)

	log.Debug().
		Str("albumId", albumID).
		Str("messageId", msg.ID).
		Int("totalMessages", len(album.Messages)).
		Msg("Message added to album buffer")

	return isFirst
}

// flushAlbum sends the aggregated album to webhook and removes it from buffer
func (ab *AlbumBuffer) flushAlbum(albumID string) {
	ab.Lock()
	album, exists := ab.albums[albumID]
	if !exists {
		ab.Unlock()
		return
	}

	// Stop timer if still running
	if album.Timer != nil {
		album.Timer.Stop()
	}

	// Remove from buffer
	delete(ab.albums, albumID)
	ab.Unlock()

	log.Info().
		Str("albumId", albumID).
		Int("totalImages", len(album.Messages)).
		Str("chat", album.ChatJID).
		Msg("Flushing album buffer, sending webhook")

	// Build webhook payload
	payload := AlbumWebhookPayload{
		Type:        "MessageAlbum",
		AlbumID:     album.AlbumID,
		Sender:      album.SenderAlt,
		SenderLid:   album.SenderJID,
		Chat:        album.ChatJID,
		Caption:     album.Caption,
		Timestamp:   album.Timestamp.Format(time.RFC3339),
		TotalImages: len(album.Messages),
		Images:      album.Messages,
	}

	// Send webhook using existing infrastructure
	if album.MyCli != nil {
		postmap := make(map[string]interface{})
		postmap["type"] = "MessageAlbum"
		postmap["albumId"] = payload.AlbumID
		postmap["sender"] = payload.Sender
		postmap["senderLid"] = payload.SenderLid
		postmap["chat"] = payload.Chat
		postmap["caption"] = payload.Caption
		postmap["timestamp"] = payload.Timestamp
		postmap["totalImages"] = payload.TotalImages
		postmap["images"] = payload.Images

		sendEventWithWebHook(album.MyCli, postmap, "")
	}
}

// CancelAlbum cancels a pending album (e.g., on disconnect)
func (ab *AlbumBuffer) CancelAlbum(albumID string) {
	ab.Lock()
	defer ab.Unlock()

	if album, exists := ab.albums[albumID]; exists {
		if album.Timer != nil {
			album.Timer.Stop()
		}
		delete(ab.albums, albumID)
		log.Debug().Str("albumId", albumID).Msg("Album cancelled")
	}
}

// GetPendingCount returns the number of pending albums
func (ab *AlbumBuffer) GetPendingCount() int {
	ab.RLock()
	defer ab.RUnlock()
	return len(ab.albums)
}

// HasParentMessageKey checks if a message context contains a parent message key (album indicator)
func HasParentMessageKey(msgContext map[string]interface{}) (string, bool) {
	if msgContext == nil {
		return "", false
	}

	// Check for messageAssociation.parentMessageKey.ID
	if msgAssoc, ok := msgContext["messageAssociation"].(map[string]interface{}); ok {
		if parentKey, ok := msgAssoc["parentMessageKey"].(map[string]interface{}); ok {
			if id, ok := parentKey["ID"].(string); ok && id != "" {
				return id, true
			}
		}
	}

	return "", false
}
