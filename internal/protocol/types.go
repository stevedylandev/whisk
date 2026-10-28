package protocol

import (
	"encoding/json"
	"slices"
	"strconv"
)

// Server is the greeting frame the server sends on connect (§3.1). A later
// server frame fully replaces the previous one.
type Server struct {
	Apron        int             `json:"apron"`
	Auth         []string        `json:"auth"`
	Agent        string          `json:"agent,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Welcome      string          `json:"welcome,omitempty"`
	Signup       []string        `json:"signup,omitempty"`
	Ping         *float64        `json:"ping,omitempty"`
	Status       []string        `json:"status,omitempty"`
	Push         json.RawMessage `json:"push,omitempty"`
	Ext          json.RawMessage `json:"ext,omitempty"`
}

// HasCapability reports whether the server advertised a capability.
func (s *Server) HasCapability(name string) bool {
	return s != nil && slices.Contains(s.Capabilities, name)
}

// User is an identity on the wire (§3.3).
type User struct {
	UserID string          `json:"user_id"`
	Name   string          `json:"name,omitempty"`
	Avatar string          `json:"avatar,omitempty"`
	Roles  []string        `json:"roles,omitempty"`
	Status string          `json:"status,omitempty"`
	Seen   string          `json:"seen,omitempty"` // log_id format; only with a shown "offline" (§4.5.2)
	Ext    json.RawMessage `json:"ext,omitempty"`
}

// AuthParams is the request body for the auth method (§3.2, §4.10, §4.11).
type AuthParams struct {
	Scheme string `json:"scheme"`
	Name   string `json:"name,omitempty"`
	UserID string `json:"user_id,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Token  string `json:"token,omitempty"`

	// email and external schemes (§4.11)
	Email     string `json:"email,omitempty"`
	State     string `json:"state,omitempty"`
	ReturnURL string `json:"return_url,omitempty"`
}

// AuthResult is the result of a successful auth request.
type AuthResult struct {
	You   *User  `json:"you,omitempty"`
	Token string `json:"token,omitempty"`

	// external scheme: the sign-in page URL to open (§4.11.5)
	URL string `json:"url,omitempty"`
}

// MeParams changes the user's own profile (§3.3.2). The string fields are
// pointers so that a carried empty value can be sent: a cleared field is
// announced as its empty value, and omitempty would drop "".
type MeParams struct {
	Name   *string `json:"name,omitempty"`
	Avatar *string `json:"avatar,omitempty"`
	Status *string `json:"status,omitempty"`
}

// StringPtr returns a pointer to s, for MeParams fields.
func StringPtr(s string) *string { return &s }

// MeResult is the result of a me request.
type MeResult struct {
	You User `json:"you"`
}

// StatusParams is the status request (§4.5.3), and the status notification
// that delivers the mutes in effect for the user. Idle is false or a
// non-negative number of seconds (only clients send it); Mute is true, false,
// or a positive number of seconds. RawMessage keeps the exact union shape.
type StatusParams struct {
	RoomID string          `json:"room_id,omitempty"`
	Idle   json.RawMessage `json:"idle,omitempty"`
	Mute   json.RawMessage `json:"mute,omitempty"`
}

// RawBool returns a JSON boolean RawMessage for a status union field.
func RawBool(b bool) json.RawMessage {
	if b {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// RawNumber returns a JSON number RawMessage for a status union field.
func RawNumber(n int) json.RawMessage {
	return json.RawMessage(strconv.Itoa(n))
}

// Body is the content of a message (§3.5).
type Body struct {
	Text     string   `json:"text,omitempty"`
	Format   string   `json:"format,omitempty"`
	Mentions []string `json:"mentions,omitempty"`
	Embeds   []Embed  `json:"embeds,omitempty"`
}

// Embed is rich content attached to a message body (§4.8).
type Embed struct {
	Kind     string          `json:"kind"`
	EmbedID  string          `json:"embed_id,omitempty"`
	Title    string          `json:"title,omitempty"`
	URL      string          `json:"url,omitempty"`
	OG       json.RawMessage `json:"og,omitempty"`
	Height   int             `json:"height,omitempty"`
	HTML     string          `json:"html,omitempty"`
	Format   string          `json:"format,omitempty"`
	Text     string          `json:"text,omitempty"`
	WriteURL string          `json:"write_url,omitempty"`
}

// Message is a message snapshot (§3.5). A client sends only the fields it
// controls; the server fills in the rest and broadcasts the full snapshot.
type Message struct {
	Body       *Body           `json:"body,omitempty"`
	RoomID     string          `json:"room_id,omitempty"`
	ReplyTo    *Message        `json:"reply_to,omitempty"`
	MessageID  string          `json:"message_id,omitempty"`
	LogID      string          `json:"log_id,omitempty"`
	PrevLogID  string          `json:"prev_log_id,omitempty"`
	PrevRoomID string          `json:"prev_room_id,omitempty"`
	From       *User           `json:"from,omitempty"`
	Deleted    bool            `json:"deleted,omitempty"`
	Ext        json.RawMessage `json:"ext,omitempty"`
}

// MessageResult is the result of a message creation or save.
type MessageResult struct {
	MessageID string  `json:"message_id,omitempty"`
	Embeds    []Embed `json:"embeds,omitempty"`
}

// Room is a room record (§4.2). It mixes logged record fields with delivery
// fields that are only present in some frames.
type Room struct {
	RoomID       string          `json:"room_id"`
	LogID        string          `json:"log_id,omitempty"`
	PrevLogID    string          `json:"prev_log_id,omitempty"`
	ParentRoomID string          `json:"parent_room_id,omitempty"`
	Private      bool            `json:"private,omitempty"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	LatestLogID  string          `json:"latest_log_id,omitempty"`
	HistoryLogID *string         `json:"history_log_id,omitempty"`
	Members      []User          `json:"members,omitempty"`
	MemberCount  int             `json:"member_count,omitempty"`
	Ext          json.RawMessage `json:"ext,omitempty"`
}

// RoomRef is a bare room reference, used in left lists.
type RoomRef struct {
	RoomID string `json:"room_id"`
}

// RoomListParams filters the room_list request (§4.2.1).
type RoomListParams struct {
	Filter       string `json:"filter,omitempty"`
	ParentRoomID string `json:"parent_room_id,omitempty"`
	RoomID       string `json:"room_id,omitempty"`
	Members      bool   `json:"members,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	LatestLogID  string `json:"latest_log_id,omitempty"`
}

// RoomListResult is the result of a room_list request.
type RoomListResult struct {
	Joined    []Room    `json:"joined,omitempty"`
	NotJoined []Room    `json:"not_joined,omitempty"`
	Left      []RoomRef `json:"left,omitempty"`
	Users     []User    `json:"users,omitempty"`
}

// RoomSetParams creates or edits a room (§4.2.4).
type RoomSetParams struct {
	RoomID       string          `json:"room_id,omitempty"`
	ParentRoomID string          `json:"parent_room_id,omitempty"`
	Private      bool            `json:"private,omitempty"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	Ext          json.RawMessage `json:"ext,omitempty"`
}

// RoomSetResult is the result of a room_set request.
type RoomSetResult struct {
	RoomID string `json:"room_id"`
}

// Membership is a logged membership record (§4.2.2).
type Membership struct {
	LogID   string        `json:"log_id"`
	RoomID  string        `json:"room_id"`
	Members []MemberEntry `json:"members"`
}

// MemberEntry is one user's membership state in a membership record.
type MemberEntry struct {
	User   User `json:"user"`
	Joined bool `json:"joined"`
}

// RoomUpdateParams is the room_update notification (§4.2.3).
type RoomUpdateParams struct {
	Joined      []Room       `json:"joined,omitempty"`
	Left        []RoomRef    `json:"left,omitempty"`
	Updated     []Room       `json:"updated,omitempty"`
	Memberships []Membership `json:"memberships,omitempty"`
	Users       []User       `json:"users,omitempty"`
}

// UserParams is the user notification (§3.3.3).
type UserParams struct {
	You *User `json:"you,omitempty"`
	New *User `json:"new,omitempty"`
	Old *User `json:"old,omitempty"`
}

// HistoryParams pages a room's log (§4.3). An empty RoomID omits the key,
// which queries the combined log of every delivered room; it never goes out
// as "room_id": "".
type HistoryParams struct {
	RoomID string `json:"room_id,omitempty"`
	After  string `json:"after,omitempty"`
	Before string `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// HistoryResult is the result of a history request.
type HistoryResult struct {
	Rooms        []Room            `json:"rooms,omitempty"`
	Messages     []Message         `json:"messages,omitempty"`
	Reactions    []ReactionsParams `json:"reactions,omitempty"`
	Memberships  []Membership      `json:"memberships,omitempty"`
	FirstLogID   string            `json:"first_log_id,omitempty"`
	LastLogID    string            `json:"last_log_id,omitempty"`
	More         bool              `json:"more,omitempty"`
	LatestLogID  string            `json:"latest_log_id,omitempty"`
	HistoryLogID *string           `json:"history_log_id,omitempty"`
}

// ReactionEntry is one user's complete reaction set on a message (§4.7).
type ReactionEntry struct {
	From   User     `json:"from"`
	Emojis []string `json:"emojis"`
}

// ReactionsParams is a reactions record: the complete set of one or more users
// on a message. It is both the notification params and a history record.
type ReactionsParams struct {
	LogID     string          `json:"log_id,omitempty"`
	MessageID string          `json:"message_id"`
	RoomID    string          `json:"room_id,omitempty"`
	Reactions []ReactionEntry `json:"reactions"`
}

// ReactionsRequest is the reactions request body: the caller's complete set on
// one message (§4.7).
type ReactionsRequest struct {
	MessageID string   `json:"message_id"`
	Emojis    []string `json:"emojis"`
}

// ActivityParams is the activity notification (§4.6). A nil Typing means the
// field is absent; 0 stops typing. From is set by the server on delivery.
type ActivityParams struct {
	RoomID        string `json:"room_id"`
	From          *User  `json:"from,omitempty"`
	Typing        *int   `json:"typing,omitempty"`
	ReadMessageID string `json:"read_message_id,omitempty"`
}
