package uploader

import (
	"context"
	"strings"

	"github.com/alsosee/thumbnailer/pkg/r2"
	"github.com/charmbracelet/log"
)

type R2 struct {
	ctx  context.Context
	r2   *r2.R2
	trim string
}

func NewR2(ctx context.Context, r2 *r2.R2, trim string) *R2 {
	return &R2{
		ctx:  ctx,
		r2:   r2,
		trim: trim,
	}
}

func (r2 *R2) Upload(key string, body []byte) error {
	// R2 object key is the same as file path, relative to media directory
	key = strings.TrimPrefix(key, r2.trim)

	log.Infof("Uploading %s", key)
	return r2.r2.Upload(r2.ctx, key, body)
}
