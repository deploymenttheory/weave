// Built upon the on the Vision framework bindings:
// recognize on-screen text in a captured framebuffer and locate it in pixel
// coordinates. The image is round-tripped through a temporary PNG so
// VNImageRequestHandler can ingest it via URL (avoids hand-building a
// GImage through purego).
//go:build darwin

package ocr

import (
	"image"
	"image/png"
	"os"
	"sort"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	vision "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/vision"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// ocrMinimumConfidence mirrors OCRService's default minimumConfidence.
const ocrMinimumConfidence = 0.3

// TextObservation is one recognized text line, with its bounding box in
// pixel coordinates (top-left origin).
type TextObservation struct {
	Text       string
	Confidence float32
	Box        image.Rectangle
}

// Center returns the click point for the observation.
func (o TextObservation) Center() image.Point {
	return image.Pt((o.Box.Min.X+o.Box.Max.X)/2, (o.Box.Min.Y+o.Box.Max.Y)/2)
}

// RecognizeText runs Vision text recognition over img and returns all
// observations above the confidence floor, in top-to-bottom screen order.
func RecognizeText(img image.Image) ([]TextObservation, error) {
	// Round-trip via a temporary PNG.
	file, err := os.CreateTemp("", "weave-ocr-*.png")
	if err != nil {
		return nil, weaveerrors.ErrOCRFailed(err.Error())
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		return nil, weaveerrors.ErrOCRFailed(err.Error())
	}
	if err := file.Close(); err != nil {
		return nil, weaveerrors.ErrOCRFailed(err.Error())
	}

	width := img.Bounds().Dx()
	height := img.Bounds().Dy()

	request := vision.VNRecognizeTextRequestFromID(
		purego.Send[purego.ID](objcutil.AllocClass("VNRecognizeTextRequest"), purego.RegisterName("init")))
	request.SetRecognitionLevel(vision.VNRequestTextRecognitionLevelAccurate)
	request.SetUsesLanguageCorrection(false)
	// Revision 3 supports the latest recognition features (OCRService.swift).
	request.VNRequest.SetRevision(3)

	handler := vision.VNImageRequestHandlerFromID(objcutil.AllocClass("VNImageRequestHandler")).
		InitWithURLOptions(objcutil.NSURLFromPath(tempPath), nil)

	requests := foundation.NSArrayFromID[*vision.VNRequest](purego.Retain(purego.Send[purego.ID](
		purego.ID(purego.GetClass("NSArray")), purego.RegisterName("arrayWithObject:"), request.Ptr())))
	if _, err := handler.PerformRequestsError(requests); err != nil {
		return nil, weaveerrors.ErrOCRFailed(err.Error())
	}

	results := request.Results()
	if results == nil {
		return nil, nil
	}

	var observations []TextObservation
	count := purego.Send[uint](results.Ptr(), objcutil.SelCount)
	for i := range count {
		id := purego.Send[purego.ID](results.Ptr(), objcutil.SelObjectAtIndex, i)
		textObservation := vision.VNRecognizedTextObservationFromID(purego.Retain(id))

		candidates := textObservation.TopCandidates(1)
		if candidates == nil || purego.Send[uint](candidates.Ptr(), objcutil.SelCount) == 0 {
			continue
		}
		candidate := vision.VNRecognizedTextFromID(purego.Retain(
			purego.Send[purego.ID](candidates.Ptr(), objcutil.SelObjectAtIndex, uint(0))))

		confidence := candidate.Confidence()
		if confidence < ocrMinimumConfidence {
			continue
		}

		// Vision bounding boxes are normalized with a bottom-left origin;
		// convert to top-left pixel coordinates (OCRService.screenRect).
		box := textObservation.BoundingBox()
		x := box.Origin.X * float64(width)
		y := (1 - box.Origin.Y - box.Size.Height) * float64(height)
		w := box.Size.Width * float64(width)
		h := box.Size.Height * float64(height)

		observations = append(observations, TextObservation{
			Text:       objcutil.GoStr(candidate.String()),
			Confidence: confidence,
			Box:        image.Rect(int(x), int(y), int(x+w), int(y+h)),
		})
	}

	// Top-to-bottom order, as OCRService sorts matches.
	sort.Slice(observations, func(i, j int) bool {
		return observations[i].Box.Min.Y < observations[j].Box.Min.Y
	})
	return observations, nil
}

// FindAllText returns every observation containing text (case-insensitive),
// top-to-bottom.
// FindAllText returns observations matching text, top-to-bottom. Exact
// (case-insensitive, whitespace-trimmed) matches are strongly preferred: when
// any exist, only they are returned. This avoids substring false positives —
// e.g. searching "Agree" must hit the "Agree" button and not the "Disagree"
// button (which contains "agree") nor "AGREE"/"AGREEING" in license body
// text, and searching "English" hits the plain "English" row rather than
// "English (UK)". Only when there is no exact match does it fall back to
// substring matching.
func FindAllText(text string, observations []TextObservation) []TextObservation {
	needle := strings.ToLower(strings.TrimSpace(text))
	var exact, substring []TextObservation
	for _, observation := range observations {
		candidate := strings.ToLower(strings.TrimSpace(observation.Text))
		switch {
		case candidate == needle:
			exact = append(exact, observation)
		case strings.Contains(candidate, needle):
			substring = append(substring, observation)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return substring
}

// FindText returns the first (topmost) match, if any.
func FindText(text string, observations []TextObservation) (TextObservation, bool) {
	matches := FindAllText(text, observations)
	if len(matches) == 0 {
		return TextObservation{}, false
	}
	return matches[0], true
}
