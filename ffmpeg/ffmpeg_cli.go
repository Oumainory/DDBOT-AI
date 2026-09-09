//go:build !cgo
// +build !cgo

package ffmpeg

func ConvMediaWithProxy(url, outputPath, proxyURL, mediaType string) error {
	return convMediaWithExternalCommand(url, outputPath, proxyURL, mediaType)
}
