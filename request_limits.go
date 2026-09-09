package main

import (
	"bytes"
	"time"

	"github.com/valyala/fasthttp"
)

const (
	backendReadTimeout                = 60 * time.Second
	studioUploadMinimumBytesPerSecond = 256 * 1024
	studioUploadReadTimeoutOverhead   = 30 * time.Second
	studioUploadReadTimeoutCeiling    = 10 * time.Minute
	studioSourceUploadPath            = "/api/studio/v1/sessions/from-upload"
)

// requestReadConfig keeps the existing request deadline for ordinary traffic,
// while allowing a bounded amount of time for a real Studio source body to
// arrive. Fiber's global ReadTimeout is applied while fasthttp reads the full
// multipart body, before any route middleware can run. The Studio deadline is
// derived from the declared body size and is still capped; it is not a blanket
// increase to the application's handler or worker timeout.
func requestReadConfig(header *fasthttp.RequestHeader) fasthttp.RequestConfig {
	if isStudioSourceUpload(header) {
		return fasthttp.RequestConfig{
			ReadTimeout: studioUploadReadTimeout(header.ContentLength()),
		}
	}

	return fasthttp.RequestConfig{ReadTimeout: backendReadTimeout}
}

func isStudioSourceUpload(header *fasthttp.RequestHeader) bool {
	if header == nil || !bytes.Equal(header.Method(), []byte("POST")) {
		return false
	}

	requestURI := header.RequestURI()
	if queryStart := bytes.IndexByte(requestURI, '?'); queryStart >= 0 {
		requestURI = requestURI[:queryStart]
	}

	return bytes.Equal(requestURI, []byte(studioSourceUploadPath))
}

func studioUploadReadTimeout(contentLength int) time.Duration {
	if contentLength <= 0 {
		return studioUploadReadTimeoutCeiling
	}

	bytesToRead := int64(contentLength)
	bytesPerSecond := int64(studioUploadMinimumBytesPerSecond)
	transferSeconds := (bytesToRead + bytesPerSecond - 1) / bytesPerSecond
	timeout := time.Duration(transferSeconds)*time.Second + studioUploadReadTimeoutOverhead

	if timeout < backendReadTimeout {
		return backendReadTimeout
	}
	if timeout > studioUploadReadTimeoutCeiling {
		return studioUploadReadTimeoutCeiling
	}
	return timeout
}
