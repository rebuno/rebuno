package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func ComputeArgsHash(args []byte) (string, error) {
	canon, err := CanonicalizeJSON(args)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(canon)
	return hex.EncodeToString(h[:]), nil
}

func ComputeStepID(execID uuid.UUID, kind domain.StepKind, target, argsHash string, occurrence int) string {
	fields := []string{
		execID.String(),
		string(kind),
		target,
		argsHash,
		strconv.Itoa(occurrence),
	}
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "%d:%s", len(f), f)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

func CanonicalizeJSON(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return []byte("null"), nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicalize decode: %w", err)
	}
	return marshalCanonical(v)
}

func marshalCanonical(v any) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			bk, _ := json.Marshal(k)
			b.Write(bk)
			b.WriteByte(':')
			bv, err := marshalCanonical(x[k])
			if err != nil {
				return nil, err
			}
			b.Write(bv)
		}
		b.WriteByte('}')
		return []byte(b.String()), nil
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, el := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			be, err := marshalCanonical(el)
			if err != nil {
				return nil, err
			}
			b.Write(be)
		}
		b.WriteByte(']')
		return []byte(b.String()), nil
	case json.Number:
		return []byte(x.String()), nil
	case string:
		return json.Marshal(x)
	case bool:
		return json.Marshal(x)
	case nil:
		return []byte("null"), nil
	default:
		return json.Marshal(x)
	}
}
