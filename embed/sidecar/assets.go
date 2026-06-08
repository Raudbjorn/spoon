// Package sidecarassets embeds the Python behavioral-embeddings sidecar source
// so that an installed spoon/spn binary (e.g. via `go install`, with no repo
// checkout present) can lay the sidecar files down on disk and run it as a
// systemd service. The Python files remain the single source of truth — this
// package only embeds them; it carries no Go logic.
package sidecarassets

import _ "embed"

// ServerPy is the FastAPI sidecar server (embed/sidecar/server.py).
//
//go:embed server.py
var ServerPy string

// Requirements is the pinned dependency set (embed/sidecar/requirements.txt).
//
//go:embed requirements.txt
var Requirements string

// Dockerfile builds the self-contained sidecar image (embed/sidecar/Dockerfile).
//
//go:embed Dockerfile
var Dockerfile string

// ServiceTemplate is the canonical systemd unit template
// (embed/sidecar/spoon-sidecar.service). The installer renders a concrete unit
// rather than using this verbatim; it is embedded for the hand-install path and
// as documentation of the canonical shape.
//
//go:embed spoon-sidecar.service
var ServiceTemplate string
