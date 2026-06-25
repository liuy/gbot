package llm

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// API_IMAGE_MAX_BASE64_SIZE is the maximum base64-encoded image size enforced
// by the Anthropic API. Source: constants/apiLimits.ts:22.
const API_IMAGE_MAX_BASE64_SIZE = 5 * 1024 * 1024 // 5 MB

// OversizedImage records one image that exceeded the API base64 size limit.
// Source: utils/imageValidation.ts:10-13.
type OversizedImage struct {
	Index int
	Size  int
}

// ImageSizeError is returned when one or more images exceed the API size limit.
// Source: utils/imageValidation.ts:18-37.
type ImageSizeError struct {
	OversizedImages []OversizedImage
	MaxSize         int
	msg             string
}

func (e *ImageSizeError) Error() string { return e.msg }

func NewImageSizeError(oversized []OversizedImage, maxSize int) *ImageSizeError {
	var message string
	first := oversized[0]
	if len(oversized) == 1 {
		message = fmt.Sprintf(
			"Image base64 size (%s) exceeds API limit (%s). Please resize the image before sending.",
			formatFileSize(first.Size), formatFileSize(maxSize),
		)
	} else {
		parts := make([]string, 0, len(oversized))
		for _, img := range oversized {
			parts = append(parts, fmt.Sprintf("Image %d: %s", img.Index, formatFileSize(img.Size)))
		}
		message = fmt.Sprintf(
			"%d images exceed the API limit (%s): %s. Please resize these images before sending.",
			len(oversized), formatFileSize(maxSize), strings.Join(parts, ", "),
		)
	}
	return &ImageSizeError{OversizedImages: oversized, MaxSize: maxSize, msg: message}
}

// formatFileSize mirrors TS formatFileSize (format.ts:9-26): sizes below 1KB
// render as raw bytes; the trailing ".0" from the one-decimal format is stripped
// from the number before the unit is appended.
func formatFileSize(sizeInBytes int) string {
	kb := float64(sizeInBytes) / 1024
	if kb < 1 {
		return fmt.Sprintf("%d bytes", sizeInBytes)
	}
	if kb < 1024 {
		return stripTrailingZero(fmt.Sprintf("%.1f", kb)) + "KB"
	}
	mb := kb / 1024
	if mb < 1024 {
		return stripTrailingZero(fmt.Sprintf("%.1f", mb)) + "MB"
	}
	gb := mb / 1024
	return stripTrailingZero(fmt.Sprintf("%.1f", gb)) + "GB"
}

func stripTrailingZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

// ValidateImagesForAPI walks user messages and returns ImageSizeError if any
// base64 image block exceeds API_IMAGE_MAX_BASE64_SIZE.
// Source: utils/imageValidation.ts:73-103. The API limit applies to the
// base64 string length, not the decoded raw bytes.
func ValidateImagesForAPI(messages []types.Message) error {
	var oversized []OversizedImage
	imageIndex := 0

	for _, msg := range messages {
		// Go's []types.Message uses flat {role, content}; the TS wrapper checks
		// m.type === 'user', which maps to msg.Role == RoleUser here.
		if msg.Role != types.RoleUser {
			continue
		}

		for _, block := range msg.Content {
			if block.Type != types.ContentTypeImage {
				continue
			}
			if block.Source == nil || block.Source.Type != "base64" {
				continue
			}
			imageIndex++
			base64Size := len(block.Source.Data)
			if base64Size > API_IMAGE_MAX_BASE64_SIZE {
				oversized = append(oversized, OversizedImage{Index: imageIndex, Size: base64Size})
			}
		}
	}

	if len(oversized) > 0 {
		return NewImageSizeError(oversized, API_IMAGE_MAX_BASE64_SIZE)
	}
	return nil
}
