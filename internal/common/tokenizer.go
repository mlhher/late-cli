package common

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkoukk/tiktoken-go"
)

//go:embed assets/cl100k_base.tiktoken.gz
var cl100kGz []byte

// embeddedBpeLoader serves the cl100k_base vocabulary from an embedded,
// gzip-compressed asset so token counting works fully offline (no runtime
// network fetch). It replaces tiktoken-go's default HTTP loader, which would
// otherwise download the vocab from a remote blob on first use.
type embeddedBpeLoader struct{}

// LoadTiktokenBpe ignores the requested URL and always returns the embedded
// cl100k_base ranks. Late only ever needs cl100k_base for its token estimates.
func (embeddedBpeLoader) LoadTiktokenBpe(_ string) (map[string]int, error) {
	gz, err := gzip.NewReader(bytes.NewReader(cl100kGz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, err
	}

	ranks := make(map[string]int, 100256)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, err
		}
		rank, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}

func init() {
	// Override tiktoken-go's network loader with the embedded vocab.
	tiktoken.SetBpeLoader(embeddedBpeLoader{})
	// Warm up BPE asynchronously in the background so main thread / TUI startup is instant.
	go func() {
		_, _ = loadBPE()
	}()
}

var (
	bpeEnc    *tiktoken.Tiktoken
	bpeEncErr error
	bpeReady  atomic.Bool
	bpeMu     sync.Mutex
)

func loadBPE() (*tiktoken.Tiktoken, error) {
	if bpeReady.Load() {
		return bpeEnc, bpeEncErr
	}
	bpeMu.Lock()
	defer bpeMu.Unlock()
	if !bpeReady.Load() {
		bpeEnc, bpeEncErr = tiktoken.GetEncoding("cl100k_base")
		bpeReady.Store(true)
	}
	return bpeEnc, bpeEncErr
}

// bpe returns a cached cl100k_base BPE encoder backed by the embedded vocab.
func bpe() (*tiktoken.Tiktoken, error) {
	return loadBPE()
}

// bpeIfReady returns the loaded BPE encoder, or nil if still loading in the background.
func bpeIfReady() *tiktoken.Tiktoken {
	if bpeReady.Load() {
		return bpeEnc
	}
	return nil
}
