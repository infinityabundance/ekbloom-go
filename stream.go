package ekbloom

import (
	"io"

	"github.com/infinityabundance/ekbloom-go/internal/ekb"
)

// NewStreamBuilder starts a bounded-memory stream container written to sink.
func NewStreamBuilder(sink io.Writer, opts StreamOptions) (*StreamBuilder, error) {
	return ekb.NewStreamBuilder(sink, opts)
}

// OpenStream opens an in-memory stream container for reading.
func OpenStream(data []byte) (*StreamReader, error) { return ekb.OpenStream(data) }

// DefaultStreamOptions returns the streaming defaults.
func DefaultStreamOptions() StreamOptions { return ekb.DefaultStreamOptions() }
