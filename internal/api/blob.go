package api

// Blob wire contract for the content-addressed file store (see internal/blobs).
// Files are referenced from prompts by an @blob(<hex-prefix>) token; the daemon
// owns the store under <SESH_HOME>/blobs and serves these over the same router as
// everything else (so blob ops route cross-machine over http/ssh like tickets).

// BlobInfo is one stored blob's metadata (the `blob ls` row).
type BlobInfo struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// AddBlobRequest is the body of POST /v1/blobs. Data is the raw bytes (base64 in
// JSON). Name is the original filename, preserved so the stored path has a real
// extension for the agent that reads it.
type AddBlobRequest struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// BlobResponse wraps a single blob (the add/info reply).
type BlobResponse struct {
	Schema int      `json:"schema"`
	Blob   BlobInfo `json:"blob"`
}

// BlobListResponse is returned by GET /v1/blobs.
type BlobListResponse struct {
	Schema int        `json:"schema"`
	Blobs  []BlobInfo `json:"blobs"`
}

// DeleteBlobRequest is the body of POST /v1/blobs/delete.
type DeleteBlobRequest struct {
	Hash string `json:"hash"`
}

// BlobPathResponse is returned by GET /v1/blobs/path?hash= — the blob's absolute
// path ON THE SERVING DAEMON (what a token expands to there).
type BlobPathResponse struct {
	Schema int    `json:"schema"`
	Hash   string `json:"hash"`
	Path   string `json:"path"`
}

// ExpandBlobsRequest is the body of POST /v1/blobs/expand.
type ExpandBlobsRequest struct {
	Text string `json:"text"`
}

// ExpandBlobsResponse carries the prompt with every @blob(…) token replaced by the
// blob's absolute path on the serving daemon. A token resolving to no blob is a
// loud error, not a passthrough.
type ExpandBlobsResponse struct {
	Schema int    `json:"schema"`
	Text   string `json:"text"`
}
