package api

import (
	"net/http"

	"github.com/rebuno/rebuno/internal/kernel"
)

type toolHandlers struct {
	kernel *kernel.Kernel
}

// list returns the kernel's currently advertised tool schemas, optionally
// filtered by prefix (e.g. ?prefix=x returns x.* tools).
func (h *toolHandlers) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	schemas := h.kernel.ListTools(prefix)
	writeJSON(w, http.StatusOK, schemas)
}
