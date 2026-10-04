//go:build !linux

package ownerfile

import (
	"errors"
	"io"
)

const DefaultsMarker = "-- Managed by xivstream: Ghostty first-run defaults v1.\n"
const UnitMarker = "# Managed by xivstream; never restarted automatically on re-apply.\n"
const Protocol = "xivstream-owner-file-v1"

type Metadata struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int    `json:"size,omitempty"`
}

func Serve([]string, io.Reader, io.Writer) error {
	return errors.New("owner-file helper requires Linux")
}
