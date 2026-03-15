//go:build embed_slipstream && linux && arm64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-linux-arm64
var BinaryData []byte
