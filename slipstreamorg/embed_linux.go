//go:build embed_slipstream && linux && amd64

package slipstreamorg

import _ "embed"

//go:embed slipstream-client-linux-amd64
var BinaryData []byte
