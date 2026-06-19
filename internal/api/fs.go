package api

// FsEntry is one immediate subdirectory of a listed path.
type FsEntry struct {
	Name string `json:"name"` // the directory's basename
	Path string `json:"path"` // its path, rendered ~-relative to the daemon's home
}

// FsListResponse is the result of GET /v1/fs/list?path=… — the immediate
// SUBDIRECTORIES of an allow-listed (home-rooted) path on the serving daemon's host.
// Deliberately generic and policy-free (no boxyard knowledge): the caller decides
// which roots mean what (e.g. ~/dev = boxes, ~/mysetup = mysetup folders).
type FsListResponse struct {
	Schema  int       `json:"schema"`
	Path    string    `json:"path"` // the resolved root that was listed (~-relative)
	Entries []FsEntry `json:"entries"`
}
