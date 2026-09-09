package ffmpeg

import (
	"fmt"
	"os"
	"os/exec"
)

// convMediaWithExternalCommand invokes the separately packaged FFmpeg
// executable. Keeping this boundary in a non-CGO file makes both CGO and
// non-CGO Go builds use the same process-level runtime contract.
func convMediaWithExternalCommand(url, outputPath, proxyURL, mediaType string) error {
	args := []string{"-v", "error", "-i", url, "-f", mediaType, outputPath}
	if mediaType == "mp4" {
		args = []string{"-v", "error", "-i", url, "-c", "copy", "-movflags", "+faststart", "-f", mediaType, outputPath}
	}

	cmd := exec.Command("ffmpeg", args...)
	if proxyURL != "" {
		cmd.Env = append(os.Environ(),
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
			"rw_timeout=30000000",
		)
	}
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg CLI failed: %w", err)
	}
	return nil
}
