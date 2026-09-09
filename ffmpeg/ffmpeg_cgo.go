//go:build cgo
// +build cgo

package ffmpeg

// ConvMediaWithProxy intentionally uses the same external FFmpeg process as
// the non-CGO build. This file exists only to keep the upstream build-tag
// surface stable; no FFmpeg library, C header, or C linker flag is involved.
func ConvMediaWithProxy(url, outputPath, proxyURL, mediaType string) error {
	return convMediaWithExternalCommand(url, outputPath, proxyURL, mediaType)
}
