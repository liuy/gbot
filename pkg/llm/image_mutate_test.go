package llm

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestImageMaxBase64Size(t *testing.T) {
	t.Parallel()
	if API_IMAGE_MAX_BASE64_SIZE != 5*1024*1024 {
		t.Errorf("API_IMAGE_MAX_BASE64_SIZE = %d, want %d", API_IMAGE_MAX_BASE64_SIZE, 5*1024*1024)
	}
}

// One value per branch boundary: the raw-bytes floor, each unit crossing, and
// the trailing-zero strip at every magnitude.
func TestFormatFileSizeBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		size int
		want string
	}{
		{1023, "1023 bytes"},
		{1024, "1KB"}, // 1.0 → strip
		{1536, "1.5KB"},
		{1047552, "1023KB"}, // last KB value below 1MB
		{1048576, "1MB"},    // first MB value (1023KB + 1)
		{1023 * 1024 * 1024, "1023MB"},
		{1073741824, "1GB"},           // first GB value
		{1075 * 1024 * 1024, "1GB"},   // 1.0498GB still renders as 1GB
		{1076 * 1024 * 1024, "1.1GB"}, // 1.0508GB crosses the 1.05 rounding edge
	}
	for _, tc := range cases {
		if got := formatFileSize(tc.size); got != tc.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

func oversizedImage(size int) types.ContentBlock {
	return types.ContentBlock{
		Type:   types.ContentTypeImage,
		Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: strings.Repeat("a", size)},
	}
}

// A single oversized image names its 1-based position among counted images.
func TestNewImageSizeError_MultiImageMessage(t *testing.T) {
	t.Parallel()
	err := NewImageSizeError([]OversizedImage{{Index: 1, Size: 6000000}, {Index: 2, Size: 7000000}}, 5242880)
	want := "2 images exceed the API limit (5MB): Image 1: 5.7MB, Image 2: 6.7MB. Please resize these images before sending."
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if len(err.OversizedImages) != 2 || err.MaxSize != 5242880 {
		t.Errorf("fields = %+v, want 2 oversized at max 5242880", err)
	}
}

func TestValidateImagesForAPI_SkipsNonUserMessages(t *testing.T) {
	t.Parallel()
	big := oversizedImage(API_IMAGE_MAX_BASE64_SIZE + 1)
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{big}},
		{Role: types.RoleUser, Content: []types.ContentBlock{big}},
	}
	err := ValidateImagesForAPI(messages)
	isz, ok := err.(*ImageSizeError)
	if !ok {
		t.Fatalf("err = %v, want *ImageSizeError", err)
	}
	if len(isz.OversizedImages) != 1 || isz.OversizedImages[0].Index != 1 {
		t.Errorf("oversized = %+v, want exactly [Index:1] — the assistant turn must not count", isz.OversizedImages)
	}
}

func TestValidateImagesForAPI_NonImageBlocksSkipped(t *testing.T) {
	t.Parallel()
	// A text block carrying a base64-looking source must not be counted as an
	// image: only the image block's index advances.
	textWithSource := types.ContentBlock{
		Type:   types.ContentTypeText,
		Text:   "hi",
		Source: &types.ImageSource{Type: "base64", Data: strings.Repeat("b", API_IMAGE_MAX_BASE64_SIZE+1)},
	}
	messages := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			textWithSource,
			oversizedImage(API_IMAGE_MAX_BASE64_SIZE + 1),
		},
	}}
	err := ValidateImagesForAPI(messages)
	isz, ok := err.(*ImageSizeError)
	if !ok {
		t.Fatalf("err = %v, want *ImageSizeError", err)
	}
	if len(isz.OversizedImages) != 1 || isz.OversizedImages[0].Index != 1 {
		t.Errorf("oversized = %+v, want exactly [Index:1]", isz.OversizedImages)
	}
}

func TestValidateImagesForAPI_SourcelessImageSkipped(t *testing.T) {
	t.Parallel()
	messages := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage},
			oversizedImage(API_IMAGE_MAX_BASE64_SIZE + 1),
		},
	}}
	err := ValidateImagesForAPI(messages)
	isz, ok := err.(*ImageSizeError)
	if !ok {
		t.Fatalf("err = %v, want *ImageSizeError", err)
	}
	if len(isz.OversizedImages) != 1 || isz.OversizedImages[0].Index != 1 {
		t.Errorf("oversized = %+v, want exactly [Index:1] — the sourceless image must not count", isz.OversizedImages)
	}
}

// The limit applies to strictly-larger images: at-limit passes, limit+1 fails.
func TestValidateImagesForAPI_LimitBoundary(t *testing.T) {
	t.Parallel()
	atLimit := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{oversizedImage(API_IMAGE_MAX_BASE64_SIZE)}}}
	if err := ValidateImagesForAPI(atLimit); err != nil {
		t.Errorf("at-limit image returned %v, want nil", err)
	}
	overLimit := []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{oversizedImage(API_IMAGE_MAX_BASE64_SIZE + 1)}}}
	err := ValidateImagesForAPI(overLimit)
	isz, ok := err.(*ImageSizeError)
	if !ok {
		t.Fatalf("over-limit image err = %v, want *ImageSizeError", err)
	}
	if len(isz.OversizedImages) != 1 || isz.OversizedImages[0].Size != API_IMAGE_MAX_BASE64_SIZE+1 {
		t.Errorf("oversized = %+v, want one entry at limit+1", isz.OversizedImages)
	}
}
