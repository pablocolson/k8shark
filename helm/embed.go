// Package helm embeds the k8shark Helm chart into the binary so the CLI can
// deploy without any external chart repository.
package helm

import "embed"

// Chart holds the packaged chart tree rooted at "k8shark/".
//
//go:embed all:k8shark
var Chart embed.FS
