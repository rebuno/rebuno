package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/rebuno/rebuno/internal/domain"
)

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: request body is required", domain.ErrValidation)
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(v); err != nil {
		if err.Error() == "http: request body too large" {
			return fmt.Errorf("%w: request body too large", domain.ErrValidation)
		}
		return fmt.Errorf("%w: invalid JSON: %s", domain.ErrValidation, err.Error())
	}
	return nil
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}

func queryString(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}
