package api

import (
	"context"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// Thumbnail is a running machine's screen, width pixels wide, for the
// list (its PNG base64 in JSON).
func (s *Service) Thumbnail(id, uuid string, width int) (hv.Screenshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Thumbnail(ctx, id, uuid, width)
}
