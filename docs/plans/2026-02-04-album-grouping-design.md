# Album Grouping Design for Wuzapi

## Overview

Implement album/gallery message grouping in wuzapi to aggregate multiple images sent together (as a WhatsApp album) into a single webhook payload instead of sending separate webhooks for each image.

## Problem Statement

When a user sends multiple images as a gallery/album in WhatsApp:
- WhatsApp sends each image as a **separate message** with a shared `parentMessageKey`
- Currently, wuzapi sends **3 separate webhooks** to n8n
- n8n only processes **1 of the 3** due to rapid concurrent requests
- User wants to receive **all images in 1 webhook**

## Solution

Implement a **smart buffering system** that:
1. Detects album messages via `messageAssociation.parentMessageKey`
2. Buffers related messages for a configurable wait time
3. Aggregates them into a single `MessageAlbum` webhook

## Technical Specifications

### Detection Logic

```
Message received
    ↓
Check for `messageAssociation.parentMessageKey` in message context
    ├─ NOT PRESENT → Send webhook immediately (real-time)
    └─ PRESENT → Add to album buffer
                    ↓
                 Start/reset 5-second timer for this albumId
                    ↓
                 After timeout, aggregate and send single webhook
```

### Configuration

| Parameter | Value | Notes |
|-----------|-------|-------|
| Wait Time | 5 seconds | Configurable via environment variable |
| Detection | Smart | Only buffer messages with `parentMessageKey` |
| Media Types | image, video, document | All media types that can be sent as album |

### Buffer Structure

```go
type AlbumBuffer struct {
    sync.RWMutex
    albums map[string]*AlbumData  // key: parentMessageKey (albumId)
}

type AlbumData struct {
    AlbumID     string
    ChatJID     string
    SenderJID   string
    Caption     string
    Timestamp   time.Time
    Messages    []AlbumMessage
    Timer       *time.Timer
}

type AlbumMessage struct {
    ID       string
    URL      string
    Base64   string
    MimeType string
    FileName string
    S3Data   map[string]interface{}
}
```

### Output Format

```json
{
  "type": "MessageAlbum",
  "albumId": "AC2BDB085FB46CF683158B6F70CFE9A4",
  "sender": "62817147127@s.whatsapp.net",
  "senderLid": "8353895989430@lid",
  "chat": "120363213309213777@g.us",
  "caption": "Alhamdulillah Setelah terapi warna disekitar 5 jam cek lagi udah bisa terdeteksi jadi 8,0",
  "timestamp": "2026-02-04T15:42:57+07:00",
  "totalImages": 3,
  "images": [
    {
      "id": "ACCA35145784E3A9A1875263B15FEC5A",
      "url": "https://s3.example.com/image1.jpg",
      "base64": "...",
      "mimeType": "image/jpeg",
      "fileName": "ACCA35145784E3A9A1875263B15FEC5A.jpg"
    },
    {
      "id": "ACF7E85FD9AD149587C46ECA19039091",
      "url": "https://s3.example.com/image2.jpg",
      "base64": "...",
      "mimeType": "image/jpeg",
      "fileName": "ACF7E85FD9AD149587C46ECA19039091.jpg"
    },
    {
      "id": "ACF4186FE2245A09DAE26C7C3D12152B",
      "url": "https://s3.example.com/image3.jpg",
      "base64": "...",
      "mimeType": "image/jpeg",
      "fileName": "ACF4186FE2245A09DAE26C7C3D12152B.jpg"
    }
  ]
}
```

## Implementation Plan

### Phase 1: Album Buffer Infrastructure
1. Create `album_buffer.go` with buffer data structures
2. Implement thread-safe album storage with mutex
3. Add timer management for each album

### Phase 2: Message Detection
1. Modify `myEventHandler` in `wmiau.go`
2. Extract `messageAssociation.parentMessageKey` from incoming messages
3. Route album messages to buffer, non-album messages direct to webhook

### Phase 3: Aggregation & Webhook
1. Implement album aggregation logic
2. Create `MessageAlbum` webhook payload format
3. Send aggregated webhook after timeout

### Phase 4: Configuration
1. Add `ALBUM_WAIT_TIME` environment variable (default: 5s)
2. Add `ALBUM_GROUPING_ENABLED` toggle (default: true)
3. Update documentation

## Files to Modify/Create

| File | Action | Description |
|------|--------|-------------|
| `album_buffer.go` | CREATE | New file for album buffering logic |
| `wmiau.go` | MODIFY | Add album detection in event handler |
| `constants.go` | MODIFY | Add album-related constants |
| `main.go` | MODIFY | Initialize album buffer, add env vars |
| `README.md` | MODIFY | Document new feature |

## Environment Variables

```bash
# Enable/disable album grouping (default: true)
WUZAPI_ALBUM_GROUPING=true

# Wait time in seconds before sending album webhook (default: 5)
WUZAPI_ALBUM_WAIT_SECONDS=5
```

## Backward Compatibility

- Feature is **opt-out** (enabled by default)
- Set `WUZAPI_ALBUM_GROUPING=false` to disable and keep current behavior
- Non-album messages are unaffected (sent immediately)

## Edge Cases

1. **Single image with parentMessageKey**: Wait 5s, then send as album with 1 image
2. **Album timeout during processing**: Ensure thread-safe buffer access
3. **Server restart with pending albums**: Albums in buffer will be lost (acceptable)
4. **Very large albums (10+ images)**: No limit, all will be aggregated

## Success Criteria

1. Album messages (with `parentMessageKey`) are buffered correctly
2. Single webhook sent after 5-second timeout with all images
3. Non-album messages sent immediately (no delay)
4. n8n receives complete album data in one request
5. No memory leaks from orphaned timers

## Testing Plan

1. Send single image → Should receive immediately
2. Send 2 images as album → Should receive 1 webhook with 2 images after 5s
3. Send 5 images as album → Should receive 1 webhook with 5 images after 5s
4. Send text message → Should receive immediately
5. Send album + text immediately after → Album delayed, text immediate
