//go:build embed_slipstream && darwin && arm64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-darwin-arm64
var BinaryData []byte
