package main

// bytesReader and newReq are kept in their own file so main.go reads
// linearly. Both wrap stdlib types we don't want to import in main.go
// solely for the side-effect.

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func newReq(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, body)
}
