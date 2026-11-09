package httpserve

type HTTPServer interface {
	ListenAndServe() error // Blocks.
	Close() error
}
