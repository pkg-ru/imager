package detection

import (
	"context"

	"github.com/pkg-ru/imager/domain/filemeta"
)

// PortDetector is a thin adapter over detection.Detector implementing the
// application port detector.Detector (ports/detector)
// on domain boxes filemeta.FaceInfo/ObjectInfo. It is assembled in the
// composition root and passed as generatev2.Deps.Detector.
type PortDetector struct {
	det Detector
}

// NewPortDetector wraps det; nil is returned for nil det (port disabled).
func NewPortDetector(det Detector) *PortDetector {
	if det == nil {
		return nil
	}
	return &PortDetector{det: det}
}

// Available delegates to the wrapped detector.
func (d *PortDetector) Available() bool { return d.det.Available() }

// DetectFaces converts detection.Box to filemeta.FaceInfo.
func (d *PortDetector) DetectFaces(ctx context.Context, rgb []byte, width, height int) ([]filemeta.FaceInfo, error) {
	boxes, err := d.det.DetectFaces(ctx, rgb, width, height)
	if err != nil {
		return nil, err
	}
	return facesToFilemeta(boxes), nil
}

// DetectObjects converts detection.Box to filemeta.ObjectInfo.
func (d *PortDetector) DetectObjects(ctx context.Context, rgb []byte, width, height int) ([]filemeta.ObjectInfo, error) {
	boxes, err := d.det.DetectObjects(ctx, rgb, width, height)
	if err != nil {
		return nil, err
	}
	return objectsToFilemeta(boxes), nil
}

func facesToFilemeta(boxes []Box) []filemeta.FaceInfo {
	out := make([]filemeta.FaceInfo, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, filemeta.FaceInfo{
			PixelBox:   filemeta.PixelBox{X: b.X, Y: b.Y, Width: b.W, Height: b.H},
			Confidence: b.Confidence,
		})
	}
	return out
}

func objectsToFilemeta(boxes []Box) []filemeta.ObjectInfo {
	out := make([]filemeta.ObjectInfo, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, filemeta.ObjectInfo{
			PixelBox:   filemeta.PixelBox{X: b.X, Y: b.Y, Width: b.W, Height: b.H},
			Confidence: b.Confidence,
			Label:      b.Label,
		})
	}
	return out
}

// compile-time check: PortDetector implements the application port.
var _ interface {
	DetectFaces(ctx context.Context, rgb []byte, width, height int) ([]filemeta.FaceInfo, error)
	DetectObjects(ctx context.Context, rgb []byte, width, height int) ([]filemeta.ObjectInfo, error)
	Available() bool
} = (*PortDetector)(nil)
