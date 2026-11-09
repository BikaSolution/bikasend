package site

import "context"

type Service interface {
	// Releases lists published releases, newest first.
	Releases(ctx context.Context) ([]Release, error)
	// Latest returns the newest release that is not a pre-release, or the newest release if all are.
	Latest(ctx context.Context) (Release, error)
	// DownloadURL returns a short-lived signed URL for one asset of one release.
	DownloadURL(ctx context.Context, tag, name string) (string, error)
}
