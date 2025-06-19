package handlers

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/google/uuid"
	"github.com/napuu/gpsp-bot/internal/config"
	"github.com/napuu/gpsp-bot/pkg/utils"
)

type VideoPostprocessingHandler struct {
	next ContextHandler
}

func cutVideo(input, output string, startSeconds, durationSeconds float64) error {
	args := []string{}
	if startSeconds > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.4f", startSeconds))
	} else if startSeconds < 0 {
		args = append(args, "-sseof", fmt.Sprintf("%.4f", startSeconds))
	}
	args = append(args, "-i", input)
	if durationSeconds > 0 {
		args = append(args, "-t", fmt.Sprintf("%.4f", durationSeconds))
	}
	args = append(args, output)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func compressVideo(input, output string, divider int) error {
	args := []string{
		"-i", input,
		"-vf", fmt.Sprintf("scale=trunc(iw/%d)*2:trunc(ih/%d)*2", divider, divider),
		"-vcodec", "libx265",
		"-crf", "28",
		output,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func reencodeToH264(input, output string) error {
	args := []string{
		"-i", input,
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart", // helpful for web compatibility
		output,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func truncateVideo(input, output string, sizeMb int) error {
	args := []string{
		"-i", input,
		"-fs", fmt.Sprintf("%dM", sizeMb),
		"-c", "copy",
		output,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func getFileSize(input string) float64 {
	fileInfo, err := os.Stat(input)
	if err != nil {
		panic(err)
	}
	return float64(fileInfo.Size()) / (1024 * 1024)
}

// Ensure that the file is small enough
func checkAndCompress(input string, maxSizeMB float64) string {
	sizeMB := getFileSize(input)

	if sizeMB > maxSizeMB {
		slog.Debug("Big file, reducing file size")

		halfPath := fmt.Sprintf("%s.half.mp4", input)
		err := compressVideo(input, halfPath, 4)
		if err != nil {
			panic(err)
		}
		sizeMB = getFileSize(halfPath)

		if sizeMB < maxSizeMB {
			return halfPath
		}
		slog.Debug("Halved, still too big")

		quarterPath := fmt.Sprintf("%s.quarter.mp4", input)
		err = compressVideo(input, quarterPath, 8)
		if err != nil {
			panic(err)
		}
		sizeMB = getFileSize(halfPath)

		if sizeMB < maxSizeMB {
			return quarterPath
		}
		slog.Debug("Quarter, still too big")

		truncatedPath := fmt.Sprintf("%s.truncated.mp4", input)
		err = truncateVideo(quarterPath, truncatedPath, int(maxSizeMB))
		if err != nil {
			panic(err)
		}
		fmt.Println(halfPath, quarterPath, truncatedPath)

		slog.Debug("Returning truncated one")
		return truncatedPath
	}

	return input
}

func (u *VideoPostprocessingHandler) Execute(m *Context) {
	slog.Debug("Entering VideoPostprocessingHandler")
	shouldTryPostprocessing := <-m.cutVideoArgsParsed

	if m.action == DownloadVideo {
		var startSeconds, durationSeconds float64
		if shouldTryPostprocessing {
			startSeconds = <-m.startSeconds
			durationSeconds = <-m.durationSeconds
		}

		if config.IsApiVideoEnabled() {
			slog.Info("api.video is enabled, trying to use it for post-processing")
			processedPath, err := utils.UploadAndCutVideo(m.finalVideoPath, startSeconds, durationSeconds)
			if err != nil {
				slog.Error("api.video post-processing failed, falling back to ffmpeg", "error", err)
			} else {
				slog.Info("api.video post-processing successful")
				m.finalVideoPath = processedPath
				// Compression is handled by api.video, maybe just check size
				m.finalVideoPath = checkAndCompress(m.finalVideoPath, 10)
				u.next.Execute(m)
				return
			}
		}

		slog.Info("Using ffmpeg for post-processing")
		if shouldTryPostprocessing {
			videoID := uuid.New().String()
			filePath := fmt.Sprintf("/tmp/%s.mp4", videoID)
			err := cutVideo(m.finalVideoPath, filePath, startSeconds, durationSeconds)
			if err != nil {
				panic(err)
			} else {
				m.finalVideoPath = filePath
			}
		}

		if utils.FileExists(m.finalVideoPath) {
			// Reencode to H.264 to get rid of VP9
			h264Path := fmt.Sprintf("%s.h264.mp4", m.finalVideoPath)
			err := reencodeToH264(m.finalVideoPath, h264Path)
			if err != nil {
				panic(err)
			}
			m.finalVideoPath = h264Path

			m.finalVideoPath = checkAndCompress(m.finalVideoPath, 10)
		}
	}

	u.next.Execute(m)
}

func (u *VideoPostprocessingHandler) SetNext(next ContextHandler) {
	u.next = next
}
