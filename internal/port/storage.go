package port

import "context"

type ImageStorage interface {
	Put(context.Context, string, []byte) error
	Read(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}
