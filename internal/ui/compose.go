package ui

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// Sending from the composer: new messages, replies, edits (message saves,
// §4.4), server commands, and file uploads (§4.8.3).

// pendingUpload is a file read from disk waiting to be PUT to a write_url.
type pendingUpload struct {
	path     string
	name     string // title sent with the embed; may differ from path after compression
	mimeType string
	data     []byte
}

type uploadDoneMsg struct {
	path string
	err  error
}

// uploadCmd PUTs file content to a server-issued write_url (§4.8.3).
func uploadCmd(writeURL, mimeType string, data []byte, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, writeURL, bytes.NewReader(data))
		if err != nil {
			return uploadDoneMsg{path: path, err: err}
		}
		if mimeType != "" {
			req.Header.Set("Content-Type", mimeType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return uploadDoneMsg{path: path, err: err}
		}
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return uploadDoneMsg{path: path, err: fmt.Errorf("upload failed: %s", resp.Status)}
		}
		return uploadDoneMsg{path: path}
	}
}

func (m *Model) sendComposer() tea.Cmd {
	text := m.composerText()
	if text == "" {
		return nil
	}
	if err := protocol.CheckText(text); err != nil {
		// The server refuses text over 4,096 bytes (too_large); three such
		// requests in a minute would close the connection. The composer
		// keeps every byte.
		m.setStatus(err.Error(), true)
		return nil
	}
	replyTo := m.chat.replyTo
	editingID := m.chat.editingID
	m.chat.composer.Reset()
	m.chat.replyTo = ""
	m.chat.editingID = ""
	m.chat.stickBottom = true

	if editingID != "" {
		return m.saveMessage(editingID, text)
	}
	return m.dispatchText(text, replyTo)
}

// requireWrite reports whether writes are allowed here, naming what a
// read-only user needs to change: guests on a read-only server may only read
// (you.ext.permissions: {"write": false}), and whisk refuses their writes
// locally rather than spending requests the server denies.
func (m *Model) requireWrite() bool {
	if m.cur().canWrite() {
		return true
	}
	m.setStatus("you can only read here; sign in with a token to post or join rooms", true)
	return false
}

// canPostWithoutRoom reports whether a message may be sent with no room_id
// (§3.5.1): the server then posts to its default room. This is possible when
// the server has no rooms capability, or when no joined room is known yet.
func (m *Model) canPostWithoutRoom() bool {
	if m.hasCap("rooms") && len(m.cur().store.Rooms()) > 0 {
		return false
	}
	return true
}

// composeMessage builds a new-message request from composer text, adding
// mentions (§3.5.5). It returns nil when a room must be selected first.
func (m *Model) composeMessage(text string) *protocol.Message {
	roomID := m.chat.roomID
	if roomID == "" && !m.canPostWithoutRoom() {
		return nil
	}
	return &protocol.Message{
		RoomID: roomID,
		Body:   &protocol.Body{Text: text, Mentions: extractMentions(m.cur().store, roomID, text)},
	}
}

// sendMessage posts a new message, as a reply when replyTo is set.
func (m *Model) sendMessage(text, replyTo string) tea.Cmd {
	if !m.requireWrite() {
		return nil
	}
	params := m.composeMessage(text)
	if params == nil {
		m.setStatus("no room selected; join or pick a room first", true)
		return nil
	}
	if replyTo != "" {
		params.ReplyTo = &protocol.Message{MessageID: replyTo}
	}
	return requestCmd(m.cur(), "message", "message", *params)
}

// saveMessage builds a §4.4 save: it resends reply_to, mentions, and embeds
// with their embed_ids, so editing a reply keeps the relationship and editing
// an attachment keeps the embed.
func (m *Model) saveMessage(id, text string) tea.Cmd {
	if !m.requireWrite() {
		return nil
	}
	existing := m.cur().store.MessageByID(id)
	if existing == nil {
		m.setStatus("message no longer available", true)
		return nil
	}
	save := buildSave(existing, existing.RoomID, text,
		extractMentions(m.cur().store, existing.RoomID, text))
	if save == nil {
		return nil
	}
	return requestCmd(m.cur(), "message", "message", *save)
}

// sendCommand sends one /-command to the server when it advertises
// "command" (§4.1). A command is a message, so a user who may only write
// (ext:permissions) cannot run any; whisk refuses locally.
func (m *Model) sendCommand(text, replyTo string) tea.Cmd {
	if !m.requireWrite() {
		return nil
	}
	roomID := m.chat.roomID
	params := protocol.Message{
		RoomID: roomID,
		Body:   &protocol.Body{Text: text, Mentions: extractMentions(m.cur().store, roomID, text)},
	}
	// A command can carry reply_to (§4.1), e.g. to answer an agent's
	// permission prompt.
	if replyTo != "" {
		params.ReplyTo = &protocol.Message{MessageID: replyTo}
	}
	return requestCmd(m.cur(), "command", "command", params)
}

// buildSave constructs a message save (§4.4). A save replaces every client
// field with the submitted state, so everything worth keeping is resent:
// reply_to, mentions, and embeds with their embed_ids. ext merges (§4.12) and
// needs no resend.
func buildSave(existing *protocol.Message, roomID, text string, newMentions []string) *protocol.Message {
	if existing == nil {
		return nil
	}
	format := "plain"
	mentions := append([]string(nil), newMentions...)
	var embeds []protocol.Embed
	if existing.Body != nil {
		format = existing.Body.Format
		for _, id := range existing.Body.Mentions {
			if !slices.Contains(mentions, id) {
				mentions = append(mentions, id)
			}
		}
		// An embed without an embed_id is new; a save that leaves out an
		// embed_id removes the embed, so send back every one we keep.
		for _, e := range existing.Body.Embeds {
			if e.EmbedID != "" {
				embeds = append(embeds, e)
			}
		}
	}
	save := protocol.Message{
		MessageID: existing.MessageID,
		RoomID:    roomID,
		Body:      &protocol.Body{Text: text, Format: format, Mentions: mentions, Embeds: embeds},
	}
	if existing.ReplyTo != nil && existing.ReplyTo.MessageID != "" {
		save.ReplyTo = &protocol.Message{MessageID: existing.ReplyTo.MessageID}
	}
	return &save
}

// Upload size caps (§4.8.3). An image may be read up to maxImageReadSize,
// since compressImage shrinks it below maxImageUploadSize before sending;
// smaller local caps exist meanwhile. The server's own ext.media declares
// per-type max_bytes, which always wins when it is smaller.
const (
	maxUploadSize    = 20 << 20
	maxImageReadSize = 50 << 20
)

// uploadKind selects the ext.media table a file belongs in: the upload
// embeds of messages (attachments) and the /avatar command's image.
type uploadKind int

const (
	mediaUpload uploadKind = iota
	mediaAvatar
)

// readUploadFile reads a local file for an upload embed, enforcing the size
// caps (§4.8.3). Images over maxImageUploadSize are compressed to fit.
func readUploadFile(path string, media *protocol.Media, kind uploadKind) (*pendingUpload, error) {
	// "/attach ~/pic.png" and "/avatar ~/pic.png" accept a leading ~; the
	// typed path is kept for status messages.
	local := resolveLocalPath(path)
	info, err := os.Stat(local)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(local))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	maxSize := int64(maxUploadSize)
	if strings.HasPrefix(mimeType, "image/") {
		maxSize = maxImageReadSize
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("file too large (%dMB max)", maxSize>>20)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	name := filepath.Base(local)
	if len(data) > maxImageUploadSize {
		if out, outType, outName, ok := compressImage(data, mimeType, name); ok {
			data, mimeType, name = out, outType, outName
		}
	}
	if len(data) > maxUploadSize {
		return nil, fmt.Errorf("file too large (%dMB max)", maxUploadSize>>20)
	}
	if err := checkMediaKind(media, mimeType, kind, int64(len(data))); err != nil {
		return nil, err
	}
	return &pendingUpload{path: path, name: name, mimeType: mimeType, data: data}, nil
}

// checkMediaKind checks a red file against what server.ext.media (§4.8,
// extension ext:media) accepts for its kind: whether that mime type is
// accepted there, and whether it fits its max_bytes. The base path is kept,
// so a server that allows large attachments rejects the file before whisk
// spends a request the server would refuse (too_large or unsupported).
func checkMediaKind(media *protocol.Media, mimeType string, kind uploadKind, size int64) error {
	if media == nil {
		return nil
	}
	var table map[string]protocol.MediaAccepted
	switch kind {
	case mediaAvatar:
		table = media.Avatar
	default:
		table = media.Upload
	}
	if len(table) == 0 {
		return nil
	}
	accepted, ok := table[mimeType]
	if !ok {
		return fmt.Errorf("server does not accept %s here", mimeType)
	}
	if accepted.MaxBytes > 0 && size > accepted.MaxBytes {
		return fmt.Errorf("file is %d bytes; the server takes at most %d bytes here", size, accepted.MaxBytes)
	}
	return nil
}

// startUpload reads a local file and posts an upload embed, uploading the
// content to the server-issued write_url once the message is created.
func (m *Model) startUpload(path, caption string) tea.Cmd {
	if m.chat.roomID == "" && !m.canPostWithoutRoom() {
		m.setStatus("no room selected; join a room first", true)
		return nil
	}
	if !m.requireCap("embed:upload") || !m.requireWrite() {
		return nil
	}
	up, err := readUploadFile(path, protocol.ServerMedia(m.cur().store.Server), mediaUpload)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.cur().pendingUpload = up
	return requestCmd(m.cur(), "message_upload", "message", protocol.Message{
		RoomID: m.chat.roomID,
		Body: &protocol.Body{
			Text:     caption,
			Mentions: extractMentions(m.cur().store, m.chat.roomID, caption),
			Embeds:   []protocol.Embed{{Kind: "upload", Title: up.name}},
		},
	})
}
