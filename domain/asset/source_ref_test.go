package asset

import "testing"

func TestExtractSourceBestEffort(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantPath   string
		wantName   string
		wantFormat string
		wantKey    string
	}{
		{
			name:       "canonical with path",
			raw:        "/photos/photo-1-jpg/200x200@2.webp",
			wantPath:   "photos",
			wantName:   "photo-1",
			wantFormat: "jpg",
			wantKey:    "photos/photo-1.jpg",
		},
		{
			name:       "canonical no path",
			raw:        "/img-png/200x200@2.png",
			wantPath:   "",
			wantName:   "img",
			wantFormat: "png",
			wantKey:    "img.png",
		},
		{
			name:       "preset with path",
			raw:        "/photos/photo-1-jpg/thumb@2.webp",
			wantPath:   "photos",
			wantName:   "photo-1",
			wantFormat: "jpg",
			wantKey:    "photos/photo-1.jpg",
		},
		{
			name:       "preset no path",
			raw:        "/img-png/thumb.webp",
			wantPath:   "",
			wantName:   "img",
			wantFormat: "png",
			wantKey:    "img.png",
		},
		{
			name:       "uppercase format normalized",
			raw:        "/img-JPG/200x200.webp",
			wantPath:   "",
			wantName:   "img",
			wantFormat: "jpg",
			wantKey:    "img.jpg",
		},
		{
			name:       "nested path",
			raw:        "/a/b/c/photo-1-jpg/200x200.webp",
			wantPath:   "a/b/c",
			wantName:   "photo-1",
			wantFormat: "jpg",
			wantKey:    "a/b/c/photo-1.jpg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := ExtractSourceBestEffort(tt.raw)
			if ref == nil {
				t.Fatalf("ExtractSourceBestEffort(%q) = nil, want non-nil", tt.raw)
			}
			if ref.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", ref.Path, tt.wantPath)
			}
			if ref.SourceName != tt.wantName {
				t.Errorf("SourceName = %q, want %q", ref.SourceName, tt.wantName)
			}
			if ref.SourceFormat != tt.wantFormat {
				t.Errorf("SourceFormat = %q, want %q", ref.SourceFormat, tt.wantFormat)
			}
			if got := ref.Key(); got != tt.wantKey {
				t.Errorf("Key() = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

func TestExtractSourceBestEffortInvalid(t *testing.T) {
	tests := []string{
		"",
		"/no-dot-here",
		"/img-png/",             // пустой rest
		"/img-png",              // нет "/" после source
		"/img-png/thumb",        // нет output format
		"/../etc/passwd",        // traversal
		"/a/../b/img-png/x.png", // traversal в пути
		"/img-png/%2f/x.png",    // encoded separator
		"/img-png/200x200@2",    // нет output format
		"/-png/200x200.png",     // пустое имя
		"/img-/200x200.png",     // пустой формат
		"/img-png/",             // пустой rest после slash
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if ref := ExtractSourceBestEffort(raw); ref != nil {
				t.Errorf("ExtractSourceBestEffort(%q) = %+v, want nil", raw, ref)
			}
		})
	}
}

func TestSourceRefKey(t *testing.T) {
	if got := (&SourceRef{}).Key(); got != "" {
		t.Errorf("empty SourceRef Key() = %q, want empty", got)
	}
	if got := (&SourceRef{Path: "a", SourceName: "n", SourceFormat: "png"}).Key(); got != "a/n.png" {
		t.Errorf("Key() = %q, want a/n.png", got)
	}
	if got := (&SourceRef{SourceName: "n", SourceFormat: "png"}).Key(); got != "n.png" {
		t.Errorf("Key() = %q, want n.png", got)
	}
	if got := (&SourceRef{SourceName: "n"}).SourceFileName(); got != "n" {
		t.Errorf("SourceFileName() = %q, want n", got)
	}
}
