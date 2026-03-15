//go:build embed_slipstream && darwin && amd64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-darwin-amd64
var BinaryData []byte
