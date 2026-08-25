package httpapi

import (
	"context"
	"testing"
	"time"
)

func TestBuildSourceStoreEmptyKindReturnsNil(t *testing.T) {
	s, err := BuildSourceStore(context.Background(), RemoteStorageConfig{})
	if err != nil {
		t.Fatalf("BuildSourceStore: %v", err)
	}
	if s != nil {
		t.Fatal("expected nil for empty kind (FS fallback)")
	}
}

func TestBuildResultStoreEmptyKindReturnsNil(t *testing.T) {
	r, err := BuildResultStore(context.Background(), RemoteStorageConfig{})
	if err != nil {
		t.Fatalf("BuildResultStore: %v", err)
	}
	if r != nil {
		t.Fatal("expected nil for empty kind (FS fallback)")
	}
}

func TestBuildResultStorePlainFTPAllowed(t *testing.T) {
	r, err := BuildResultStore(context.Background(), RemoteStorageConfig{
		Kind: StorageFTP, Addr: "localhost:21", User: "u", Password: "p",
	})
	if err != nil {
		t.Fatalf("BuildResultStore: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil ResultStore for plain FTP")
	}
}

func TestBuildSourceStoreUnsupportedKind(t *testing.T) {
	_, err := BuildSourceStore(context.Background(), RemoteStorageConfig{Kind: "bogus"})
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}

func TestBuildResultStoreUnsupportedKind(t *testing.T) {
	_, err := BuildResultStore(context.Background(), RemoteStorageConfig{Kind: "bogus"})
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}

func TestBuildSourceStoreSFTPMissingAuth(t *testing.T) {
	_, err := BuildSourceStore(context.Background(), RemoteStorageConfig{
		Kind: StorageSFTP, Addr: "localhost:22", User: "u",
	})
	if err == nil {
		t.Fatal("expected error for SFTP without auth")
	}
}

func TestBuildSourceStoreHTTP(t *testing.T) {
	s, err := BuildSourceStore(context.Background(), RemoteStorageConfig{
		Kind:    StorageHTTP,
		BaseURL: "https://addr.site/path_to_image/",
	})
	if err != nil {
		t.Fatalf("BuildSourceStore: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil SourceStore for HTTP")
	}
}

func TestBuildSourceStoreHTTPWithOptions(t *testing.T) {
	s, err := BuildSourceStore(context.Background(), RemoteStorageConfig{
		Kind:                StorageHTTP,
		BaseURL:             "https://addr.site/path_to_image/",
		DialTimeout:         15 * time.Second,
		ReadTimeout:         45 * time.Second,
		MaxAttempts:         5,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
	})
	if err != nil {
		t.Fatalf("BuildSourceStore: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil SourceStore for HTTP")
	}
}

func TestBuildSourceStoreHTTPMissingBaseURL(t *testing.T) {
	_, err := BuildSourceStore(context.Background(), RemoteStorageConfig{Kind: StorageHTTP})
	if err == nil {
		t.Fatal("expected error for HTTP without base url")
	}
}

func TestBuildResultStoreHTTPRejected(t *testing.T) {
	_, err := BuildResultStore(context.Background(), RemoteStorageConfig{
		Kind:    StorageHTTP,
		BaseURL: "https://addr.site/path_to_image/",
	})
	if err == nil {
		t.Fatal("expected error for HTTP result store (source-only)")
	}
}
