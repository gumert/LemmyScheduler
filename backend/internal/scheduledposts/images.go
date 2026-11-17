package scheduledposts

import (
	"context"
	"errors"
	"time"
)

const (
	MaxImageBytes = 10 * 1024 * 1024
	MaxBodyImages = 10
	MaxPostImages = 11
)

var (
	ErrImageNotFound = errors.New("image not found")
	ErrImageLimit    = errors.New("post image limit reached")
	ErrImageInUse    = errors.New("image is still referenced")
)

type ImageMetadata struct {
	ID          int64     `json:"id"`
	ContentType string    `json:"content_type"`
	CreatedAt   time.Time `json:"created_at"`
}

type Image struct {
	ImageMetadata
	Data []byte
}

type ImageStore interface {
	PutTitleImage(context.Context, int64, string, []byte) (ImageMetadata, error)
	CreateBodyImage(context.Context, int64, string, []byte) (ImageMetadata, error)
	SelectTitleImage(context.Context, int64, int64) error
	GetImage(context.Context, int64, int64, bool) (Image, error)
	DeleteImage(context.Context, int64, int64, bool) error
}
