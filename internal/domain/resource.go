package domain

import (
	"encoding/json"

	"github.com/google/uuid"
)

type Resource struct {
	ExecutionID   uuid.UUID       `json:"-"`
	Key           string          `json:"key"`
	DriverID      string          `json:"driver_id"`
	Config        json.RawMessage `json:"configuration,omitempty"`
	CoverageReuse bool            `json:"coverage_reuse"`
	EverySteps    int             `json:"every_steps"`
	OnCompletion  bool            `json:"on_completion"`
	RegisteredSeq int64           `json:"registered_seq"`
	Generation    int64           `json:"generation"`
	Count         int             `json:"count"`
	Binding       json.RawMessage `json:"binding,omitempty"`
	CheckpointRef string          `json:"checkpoint_ref,omitempty"`
}

// ResourceCheckpoint covers the boundaries in [CoveredSeq, InvalidatedSeq), or
// every boundary from CoveredSeq while InvalidatedSeq is zero.
type ResourceCheckpoint struct {
	ExecutionID    uuid.UUID `json:"-"`
	Key            string    `json:"key"`
	Generation     int64     `json:"generation"`
	Ref            string    `json:"checkpoint_ref"`
	CoveredSeq     int64     `json:"covered_seq"`
	InvalidatedSeq int64     `json:"invalidated_seq,omitempty"`
}

type StepResource struct {
	Key        string `json:"key"`
	Generation int64  `json:"generation"`
	Due        bool   `json:"due,omitempty"`
}

// An empty CheckpointRef selects the driver's initial environment.
type ResourceSelection struct {
	CheckpointRef string `json:"checkpoint_ref,omitempty"`
	CheckpointSeq int64  `json:"checkpoint_seq,omitempty"`
	Covered       bool   `json:"covered"`
}

func (c ResourceCheckpoint) Covers(seq int64) bool {
	return c.CoveredSeq <= seq && (c.InvalidatedSeq == 0 || seq < c.InvalidatedSeq)
}
