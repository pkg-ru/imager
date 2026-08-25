// Package detector defines the application-level port for AI face/object
// detection.
//
// The port mirrors adapters/processor/detection.Detector contract
// but operates on domain types filemeta.FaceInfo/ObjectInfo, so the
// application layer does not import adapters. A thin adapter over
// detection.Detector is built in the composition root.
package detector

import (
	"context"

	"github.com/pkg-ru/imager/domain/filemeta"
)

// Detector detects faces and objects on an RGB image.
//
// Contract (unified with detection.Detector):
//   - DetectFaces/DetectObjects take RGB pixels (3 bytes per pixel, R,G,B
//     order; len(rgb) == width*height*3) and image dimensions; returned
//     boxes are in pixel coordinates of the source image;
//   - Available reports whether at least one model is configured.
type Detector interface {
	// DetectFaces detects faces.
	DetectFaces(ctx context.Context, rgb []byte, width, height int) ([]filemeta.FaceInfo, error)
	// DetectObjects detects objects.
	DetectObjects(ctx context.Context, rgb []byte, width, height int) ([]filemeta.ObjectInfo, error)
	// Available reports detector readiness.
	Available() bool
}
