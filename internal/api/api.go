// Package api contains the wire types shared by the flyer server and client.
package api

import "time"

type PublishRequest struct {
	HTML       string `json:"html"`
	Title      string `json:"title,omitempty"`
	Password   string `json:"password,omitempty"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
	Permanent  bool   `json:"permanent,omitempty"`
}

type PublishResponse struct {
	Slug      string     `json:"slug"`
	URL       string     `json:"url"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Protected bool       `json:"protected"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type StatsResponse struct {
	Pages     int64 `json:"pages"`
	Active    int64 `json:"active"`
	Expired   int64 `json:"expired"`
	Permanent int64 `json:"permanent"`
	Bytes     int64 `json:"bytes"`
	Keys      int64 `json:"keys"`
}

type Key struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type KeyListResponse struct {
	Keys []Key `json:"keys"`
}

type CreateKeyRequest struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

type CreateKeyResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
}

type CleanupResponse struct {
	Deleted int `json:"deleted"`
}

type DeletePageResponse struct {
	Deleted bool `json:"deleted"`
}

type Page struct {
	Slug       string     `json:"slug"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Size       int64      `json:"size"`
	Protected  bool       `json:"protected"`
	OwnerKeyID *int64     `json:"owner_key_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type PageListResponse struct {
	Pages  []Page `json:"pages"`
	Total  int64  `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type ExpirePageResponse struct {
	Expired   bool      `json:"expired"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RevokeKeyResponse struct {
	Revoked bool `json:"revoked"`
}
