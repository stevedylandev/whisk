package protocol

import "encoding/json"

// Permissions is you.ext.permissions (extension ext:permissions), as a
// server with ext:permissions carries it. A read-only guest's you carries
// {"write": false}; a missing field defaults to allowed.
type Permissions struct {
	Write      *bool `json:"write"`
	AddMembers *bool `json:"add_members"`
}

// UserPermissions parses a user's ext.permissions, or nil when the user has
// no permissions object there.
func UserPermissions(u *User) *Permissions {
	if u == nil || len(u.Ext) == 0 {
		return nil
	}
	var ext struct {
		Permissions *Permissions `json:"permissions"`
	}
	if json.Unmarshal(u.Ext, &ext) != nil || ext.Permissions == nil {
		return nil
	}
	return ext.Permissions
}

// CanWrite reports whether the user may post, react, and join rooms. Absent
// rules mean allowed.
func CanWrite(u *User) bool {
	p := UserPermissions(u)
	return p == nil || p.Write == nil || *p.Write
}

// Media is server.ext.media (extension ext:media): what the server accepts
// for attachments, avatars and custom emoji, each by mime type with its own
// byte cap. It is present only on a server advertising ext:media.
type Media struct {
	Origins []string `json:"origins,omitempty"`
	// Upload, Avatar and Emoji each list accepted mime types with a
	// max_bytes per entry.
	Upload map[string]MediaAccepted `json:"upload,omitempty"`
	Avatar map[string]MediaAccepted `json:"avatar,omitempty"`
	Emoji  map[string]MediaAccepted `json:"emoji,omitempty"`
}

// MediaAccepted is one accepted mime type in server.ext.media.
type MediaAccepted struct {
	MaxBytes int64 `json:"max_bytes"`
}

// ServerMedia parses the server frame's ext.media, or nil on a server
// without it.
func ServerMedia(s *Server) *Media {
	if s == nil || len(s.Ext) == 0 {
		return nil
	}
	var ext struct {
		Media *Media `json:"media"`
	}
	if json.Unmarshal(s.Ext, &ext) != nil {
		return nil
	}
	return ext.Media
}
